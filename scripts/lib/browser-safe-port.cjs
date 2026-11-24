'use strict';
// Chromium refuses to navigate to these ports (net::ERR_UNSAFE_PORT). A random
// port from the OS can land on one of them (seen: 4045), so fixtures retry.
const unsafe = new Set([1719, 1720, 1723, 2049, 3659, 4045, 4190, 5060, 5061, 6000, 6566,
  6665, 6666, 6667, 6668, 6669, 6679, 6697, 10080]);

async function listen(server, host = '127.0.0.1') {
  for (let attempt = 0; attempt < 100; attempt++) {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, host, () => { server.off('error', reject); resolve(); });
    });
    if (!unsafe.has(server.address().port)) return server.address().port;
    await new Promise(resolve => server.close(resolve));
  }
  throw new Error('could not get a browser-safe local port');
}

module.exports = {listen, unsafe};
