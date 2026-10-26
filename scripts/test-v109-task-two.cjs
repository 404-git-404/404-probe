// Reuse the real loopback Server/isolated SQLite authentication and off-origin firewall.
'use strict';
process.env.V109_TASK_TWO='1';
process.env.V109_REFERENCE='1';
require('./test-v109-task-one.cjs');
