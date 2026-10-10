"""Keep the standalone verifier and the seven-asset installer byte-identical."""
from pathlib import Path

root = Path(__file__).resolve().parent.parent
installer = root / 'install.sh'
source = installer.read_text(encoding='utf-8')
verifier = (root / 'scripts/verify-migration-runner.py').read_text(encoding='utf-8')
start = source.index('  # MIGRATION_VERIFIER_BEGIN')
end = source.index('  # MIGRATION_VERIFIER_END', start)
embedded = '''  # MIGRATION_VERIFIER_BEGIN
  python3 - "${temporary_directory}/${asset}" "${temporary_directory}/RELEASE-METADATA.json" "${temporary_directory}/SHA256SUMS" "${architecture}" <<'PY_MIGRATION' || die 'migration runner static verification failed; no Agent binary or service was changed'
''' + verifier + 'PY_MIGRATION\n'
installer.write_text(source[:start] + embedded + source[end:], encoding='utf-8', newline='\n')
