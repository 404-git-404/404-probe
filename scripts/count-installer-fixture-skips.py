#!/usr/bin/env python3
"""Count skip-prefix occurrences in one captured test log (including repeats)."""
from pathlib import Path
import sys

if len(sys.argv) != 2:
    raise SystemExit('usage: count-installer-fixture-skips.py TEST_LOG')
count = Path(sys.argv[1]).read_text(encoding='utf-8', errors='replace').count('INSTALLER-FIXTURE-SKIPPED:')
print(f'INSTALLER-FIXTURE-SKIPPED occurrences: {count}')
