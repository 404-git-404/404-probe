#!/usr/bin/env python3
"""Real loopback HTTPS/curl regression. Root requires installer-fixture.py.

--real-thresholds exercises >180 seconds of progress and the real 60s stall.
No production URL override or installer test environment switch is introduced.
"""
import argparse
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import pty
import shutil
import ssl
import subprocess
import tempfile
import threading
import time


ROOT = Path(__file__).resolve().parent.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--real-thresholds', action='store_true')
    args = parser.parse_args()
    if os.geteuid() == 0 and not os.environ.get('INSTALLER_FIXTURE_ACTIVE'):
        raise SystemExit('use scripts/installer-fixture.py -- python3 scripts/test-installer-download.py')
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        config = root/'openssl.conf'
        config.write_text('[req]\ndistinguished_name=dn\nx509_extensions=ext\n[dn]\n[ext]\nsubjectAltName=IP:127.0.0.1\n')
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1',
                        '-subj','/CN=127.0.0.1','-config',str(config),'-keyout',str(root/'key'),
                        '-out',str(root/'cert')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        source = (ROOT/'install.sh').read_text()
        functions = source[:source.index('\ncanonical_version() {')]
        # Test-only source copies shorten thresholds; the real run keeps 60/1024.
        functions = functions.replace('readonly DOWNLOAD_RETRIES=4', 'DOWNLOAD_RETRIES=2')
        functions = functions.replace('readonly DOWNLOAD_RETRY_MAX_SECONDS=1800', 'DOWNLOAD_RETRY_MAX_SECONDS=1800')
        functions = functions.replace('readonly DOWNLOAD_STALL_SECONDS=60', 'DOWNLOAD_STALL_SECONDS=60')
        (root/'functions.sh').write_text(functions)
        payload = b'loopback fixture\n'*4096
        state = dict(mode='',requests=[],ranges=[])

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *unused):
                pass

            def do_GET(self):
                mode = state['mode']
                state['requests'].append(self.path)
                offset = int(self.headers.get('Range','bytes=0-')[6:].split('-')[0])
                state['ranges'].append(offset)
                data = b'x'*(205*2048) if mode == 'slow-real' else payload
                if mode == 'deadline':
                    time.sleep(2)
                    try:
                        self.send_error(503)
                    except OSError:
                        pass  # the shared deadline closes this request
                    return
                self.send_response(206 if offset else 200)
                self.send_header('Content-Length',str(len(data)-offset))
                if offset:
                    self.send_header('Content-Range',f'bytes {offset}-{len(data)-1}/{len(data)}')
                self.end_headers()
                try:
                    if mode.startswith('slow'):
                        interval = 1 if mode == 'slow-real' else .02
                        for begin in range(offset,len(data),2048):
                            self.wfile.write(data[begin:begin+2048]); self.wfile.flush()
                            time.sleep(interval)
                    elif mode == 'stuck' or mode == 'resume' and len(state['requests']) == 1:
                        if mode == 'resume':
                            self.wfile.write(data[:8192]); self.wfile.flush()
                        time.sleep(75 if args.real_thresholds and mode == 'resume' else 10)
                    else:
                        self.wfile.write(data[offset:]); self.wfile.flush()
                except (OSError, ssl.SSLError):
                    pass  # curl deliberately closes stalled transfers

        server = ThreadingHTTPServer(('127.0.0.1',0),Handler)
        server.daemon_threads = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(root/'cert',root/'key')
        server.socket = context.wrap_socket(server.socket,server_side=True)
        thread = threading.Thread(target=server.serve_forever,daemon=True); thread.start()
        prefix = f'https://127.0.0.1:{server.server_port}'
        real_curl = shutil.which('curl')
        if not real_curl:
            raise RuntimeError('fixture requires curl')
        curl = root/'curl'
        curl.write_text(f'''#!/bin/bash
set -eu
url="${{@: -1}}"
route="${{url#https://}}"; route="/${{route#*/}}"
exec '{real_curl}' --cacert '{root}/cert' "${{@:1:$#-1}}" '{prefix}'"${{route}}"
''')
        curl.chmod(0o700)

        def run(mode, body, tty=False, real=False):
            state.update(mode=mode, requests=[], ranges=[])
            output = root/'asset'
            for path in (output,root/'asset.partial'):
                path.unlink(missing_ok=True)
            wrapper = f'''source '{root}/functions.sh'
export PATH='{root}':"$PATH"
DOWNLOAD_STALL_SECONDS={60 if real else 2}
{body}
'''
            started = time.monotonic()
            if tty:
                master,slave = pty.openpty()
                proc = subprocess.Popen(['bash','-c',wrapper],stdout=slave,stderr=slave)
                os.close(slave); chunks=[]
                while True:
                    try:
                        block=os.read(master,65536)
                        if not block:break
                        chunks.append(block)
                    except OSError:break
                os.close(master); rc=proc.wait(timeout=10)
                stdout=b''; stderr=b''.join(chunks)
            else:
                proc=subprocess.run(['bash','-c',wrapper],capture_output=True,timeout=330)
                rc,stdout,stderr=proc.returncode,proc.stdout,proc.stderr
            elapsed=time.monotonic()-started
            return rc,stdout,stderr,elapsed

        download = f'download_to_file "{root}/asset" https://github.com/404-git-404/404-probe/releases/download/v1.2.3/asset'
        try:
            mode = 'slow-real' if args.real_thresholds else 'slow'
            rc,out,err,elapsed=run(mode,download,real=args.real_thresholds)
            assert rc == 0 and not out and not err,(rc,out,err)
            expected = b'x'*(205*2048) if args.real_thresholds else payload
            assert (root/'asset').read_bytes() == expected
            if args.real_thresholds:assert elapsed > 180,elapsed
            print(f'PASS continuous download: {elapsed:.2f}s, real={args.real_thresholds}',flush=True)

            rc,out,err,elapsed=run('resume',download,real=args.real_thresholds)
            assert rc == 0 and (root/'asset').read_bytes() == payload,(rc,err)
            assert len(state['requests']) == 2 and state['ranges'][1] == 8192,state
            assert '下载卡住（60 秒低于 1 KB/s）'.encode() in err,err
            if args.real_thresholds:assert elapsed >= 60,elapsed
            print(f'PASS stalled download resumed at byte 8192: {elapsed:.2f}s',flush=True)

            rc,out,err,elapsed=run('stuck',download)
            assert rc != 0 and len(state['requests']) == 3,(rc,state,err)
            assert '下载卡住（60 秒低于 1 KB/s）'.encode() in err and b'README' in err
            assert not (root/'asset').exists() and not (root/'asset.partial').exists()
            print(f'PASS exhausted retries: {elapsed:.2f}s',flush=True)

            rc,out,err,elapsed=run('deadline','DOWNLOAD_RETRY_MAX_SECONDS=3; DOWNLOAD_RETRIES=0; '+
                                  f'download_release_asset v1.2.3 asset "{root}/asset"')
            assert rc != 0 and elapsed < 5,(rc,elapsed,err)
            assert any('/releases/tags/' in path for path in state['requests']),state
            assert '超过总上限 30 分钟'.encode() in err and b'README' in err,err
            assert not (root/'asset').exists() and not (root/'asset.partial').exists()
            print(f'PASS shared direct/API deadline: {elapsed:.2f}s',flush=True)

            rc,out,err,_=run('slow',download,tty=True)
            assert rc == 0 and b'#' in err,(rc,err)
            rc,out,err,_=run('slow',download)
            assert rc == 0 and out == b'' and err == b'',(rc,out,err)
            print('PASS TTY progress and byte-empty non-TTY output',flush=True)

            # Exercise the unchanged installer's checksum gate after actual HTTPS.
            binary_section=source[source.index('download_binary() ('):source.index('\nrender_local_helper() {')]
            (root/'binary-functions.sh').write_text(binary_section)
            (root/'SHA256SUMS').write_text(hashlib.sha256(b'authentic').hexdigest()+'  404-probe-server-linux-amd64\n')
            body=f'''source '{root}/binary-functions.sh'
detect_architecture() {{ printf amd64; }}
target_version() {{ printf v1.2.3; }}
download_release_asset() {{
  if [[ "$2" == SHA256SUMS ]]; then cp '{root}/SHA256SUMS' "$3";
  else download_to_file "$3" https://github.com/404-git-404/404-probe/releases/download/v1.2.3/asset; fi
}}
download_binary server '{root}/installed'
'''
            rc,out,err,_=run('complete',body)
            assert rc != 0 and b'SHA256 verification failed' in err and not (root/'installed').exists(),(rc,err)
            print('PASS tampered downloaded asset rejected',flush=True)
        finally:
            server.shutdown();server.server_close()


if __name__ == '__main__':
    main()
