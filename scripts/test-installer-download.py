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
import signal
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
        functions = source[:source.index('\nusage() {')]
        # Test-only source copies shorten thresholds; the real run keeps 60/1024.
        for old, new in (
            ('readonly DOWNLOAD_RETRIES=4', 'DOWNLOAD_RETRIES=2'),
            ('readonly DOWNLOAD_RETRY_MAX_SECONDS=1800', 'DOWNLOAD_RETRY_MAX_SECONDS=1800'),
            ('readonly DOWNLOAD_STALL_SECONDS=60', 'DOWNLOAD_STALL_SECONDS=60'),
        ):
            assert functions.count(old) == 1, f'expected one threshold anchor: {old}'
            replaced = functions.replace(old, new)
            assert replaced != functions and old not in replaced
            functions = replaced
        (root/'functions.sh').write_text(functions)
        payload = b'loopback fixture\n'*4096
        alternate_payload = b'alternate entry\n'*4096
        request_started = threading.Event()
        state = dict(mode='',requests=[],ranges=[])

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *unused):
                pass

            def do_GET(self):
                mode = state['mode']
                state['requests'].append(self.path)
                offset = int(self.headers.get('Range','bytes=0-')[6:].split('-')[0])
                state['ranges'].append(offset)
                request_number = len(state['requests'])
                data = b'x'*(205*2048) if mode == 'slow-real' else payload
                if mode in ('deadline', 'direct-deadline'):
                    time.sleep(2)
                    try:
                        self.send_error(503)
                    except OSError:
                        pass  # the shared deadline closes this request
                    return
                if mode == 'errors':
                    self.send_error(410 if request_number == 3 else 404)
                    return
                if mode == 'same-asset' and '/releases/tags/' in self.path:
                    data = b'''{
  "tag_name": "v1.2.3",
  "assets": [
    {
      "url": "https://api.github.com/repos/404-git-404/404-probe/releases/assets/42",
      "name": "asset"
    }
  ]
}'''
                if mode == 'different-source' and self.path.endswith('/raw'):
                    data = alternate_payload
                if mode == 'no-range' and offset:
                    self.send_response(200)
                    self.send_header('Content-Length', str(len(data)))
                    self.end_headers()
                    try:
                        self.wfile.write(data)
                    except OSError:
                        pass
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
                    elif mode in ('no-range', 'same-asset', 'different-source') and request_number == 1:
                        self.wfile.write(data[:8192]); self.wfile.flush()
                        self.close_connection = True
                    elif mode == 'signal':
                        self.wfile.flush()
                        request_started.set()
                        time.sleep(15)
                    elif mode == 'stuck' or mode == 'resume' and request_number == 1:
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
            assert b'curl:' not in err,err
            if args.real_thresholds:assert elapsed >= 60,elapsed
            print(f'PASS stalled download resumed at byte 8192: {elapsed:.2f}s',flush=True)

            rc,out,err,elapsed=run('stuck',download)
            assert rc != 0 and len(state['requests']) == 3,(rc,state,err)
            assert '下载卡住（60 秒低于 1 KB/s）'.encode() in err and b'README' in err
            assert err.count(b'curl: (28)') == 1,err
            assert not (root/'asset').exists() and not (root/'asset.partial').exists()
            print(f'PASS exhausted retries: {elapsed:.2f}s',flush=True)

            rc,out,err,elapsed=run('deadline','DOWNLOAD_RETRY_MAX_SECONDS=3; DOWNLOAD_RETRIES=0; '+
                                  f'download_release_asset v1.2.3 asset "{root}/asset"')
            assert rc != 0 and elapsed < 5,(rc,elapsed,err)
            assert any('/releases/tags/' in path for path in state['requests']),state
            assert err.count('超过总上限 30 分钟'.encode()) == 1 and b'README' in err,err
            assert not (root/'asset').exists() and not (root/'asset.partial').exists()
            print(f'PASS shared direct/API deadline: {elapsed:.2f}s',flush=True)

            rc,out,err,elapsed=run('direct-deadline','DOWNLOAD_RETRY_MAX_SECONDS=1; DOWNLOAD_RETRIES=0; '+
                                  f'download_release_asset v1.2.3 asset "{root}/asset"')
            assert rc != 0 and elapsed < 4,(rc,elapsed,err)
            assert len(state['requests']) == 1 and '/releases/tags/' not in state['requests'][0],state
            assert err.count('超过总上限 30 分钟'.encode()) == 1 and b'README' in err,err
            assert not (root/'asset').exists() and not (root/'asset.partial').exists()
            print(f'PASS direct deadline reports once: {elapsed:.2f}s',flush=True)

            rc,out,err,_=run('slow',download,tty=True)
            assert rc == 0 and b'#' in err,(rc,err)
            rc,out,err,_=run('slow',download)
            assert rc == 0 and out == b'' and err == b'',(rc,out,err)
            print('PASS TTY progress and byte-empty non-TTY output',flush=True)

            rc,out,err,_=run('no-range',download)
            assert rc == 0 and (root/'asset').read_bytes() == payload and not err,(rc,err,state)
            assert state['ranges'] == [0,8192,0],state
            print('PASS curl 33 restarts at byte zero',flush=True)

            release = f'download_release_asset v1.2.3 asset "{root}/asset"'
            rc,out,err,_=run('same-asset','DOWNLOAD_RETRIES=0; '+release)
            assert rc == 0 and (root/'asset').read_bytes() == payload and not err,(rc,err,state)
            assert state['ranges'] == [0,0,8192] and state['requests'][-1].endswith('/assets/42'),state
            print('PASS same release asset resumes across the API entry',flush=True)

            rc,out,err,_=run('different-source','DOWNLOAD_RETRIES=0; '+download+
                             ' https://raw.githubusercontent.com/404-git-404/404-probe/v1.2.3/raw')
            assert rc == 0 and (root/'asset').read_bytes() == alternate_payload and not err,(rc,err,state)
            assert state['ranges'] == [0,0],state
            print('PASS different source restarts at byte zero',flush=True)

            for tty in (False, True):
                rc,out,err,_=run('errors',download,tty=tty)
                assert rc != 0 and err.count(b'curl: (22)') == 1 and b'410' in err and b'404' not in err,(rc,err)
            print('PASS only the final raw curl error reaches stderr',flush=True)

            scratch = root/'signal-tmp'
            scratch.mkdir()
            for sig in (signal.SIGINT, signal.SIGTERM):
                request_started.clear()
                state.update(mode='signal', requests=[], ranges=[])
                wrapper = f"""source '{root}/functions.sh'
export PATH='{root}':"$PATH" TMPDIR='{scratch}' PROBE_404_VERSION=v1.2.3
bootstrap_latest_installer
"""
                with subprocess.Popen(['bash','-c',wrapper], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                      start_new_session=True) as process:
                    try:
                        assert request_started.wait(5), f'bootstrap did not start for {sig.name}'
                        assert list(scratch.iterdir()), 'test must interrupt an active download'
                        os.killpg(process.pid, sig)
                        assert process.wait(timeout=5) != 0, sig.name
                        # The process-group leader may exit before the installer subshell's EXIT trap.
                        deadline = time.monotonic() + 5
                        while list(scratch.iterdir()) and time.monotonic() < deadline:
                            time.sleep(.05)
                        assert not list(scratch.iterdir()), (sig.name, list(scratch.iterdir()))
                    finally:
                        if process.poll() is None:
                            os.killpg(process.pid, signal.SIGKILL)
            print('PASS SIGINT and SIGTERM remove bootstrap temporary files',flush=True)

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
