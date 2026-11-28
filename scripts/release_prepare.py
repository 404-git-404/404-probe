"""Apply the release preparation changes to a disposable candidate clone."""
import json
import re


VERSION = r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"


def version_key(version):
    if not re.fullmatch(VERSION, version):
        raise ValueError(f"Expected a stable version, got {version!r}")
    parts = tuple(map(int, version[1:].split(".")))
    if max(parts) >= 2**64:
        raise ValueError(f"Version integer overflow: {version}")
    return parts


def retarget_readme_lines(text, previous, target, protected=()):
    """Move active README commands and links from the outgoing version to the target."""
    lines = []
    for line in text.split("\n"):
        keep = (line.startswith("> " + previous + " Stable") or any(mark in line for mark in protected)
                or previous not in line
                or not re.search(r"发布说明|快速开始|PROBE_404_VERSION|Alpine|不是当前", line))
        lines.append(line if keep else line.replace(previous, target))
    return "\n".join(lines)


def prepare(root, target, start, notes):
    files = {}

    def read(name):
        if name not in files:
            files[name] = (root / name).read_bytes().decode("utf-8")
            if "\r" in files[name]:
                raise ValueError(f"{name}: expected LF")
        return files[name]

    def edit(name, pattern, replacement, count=1):
        files[name], found = re.subn(pattern, replacement, read(name), flags=re.MULTILINE)
        if found != count:
            raise ValueError(f"{name}: expected {count} matches, found {found} for {pattern}")

    installer = "install.sh"
    storage = "internal/storage/storage.go"
    previous = re.search(r'^readonly DEFAULT_VERSION="(' + VERSION + r')"$', read(installer), re.M)[1]
    if start != previous or version_key(target) <= version_key(start):
        raise ValueError(f"Expected {previous} as the start and a newer target")
    registry = json.loads(read("scripts/r31-versions.json"))
    floor_schema = registry["versions"][start]["schema"]
    target_schema = int(re.search(r"^const currentSchemaVersion = (\d+)$", read(storage), re.M)[1])
    if not 1 <= floor_schema <= target_schema:
        raise ValueError("The registered source schema exceeds the candidate schema")
    floor_version = re.search(r'compare_versions "\$1" (' + VERSION + r')\)', read(installer))[1]

    edit(installer, r'^readonly DEFAULT_VERSION="' + re.escape(previous) + '"$',
         f'readonly DEFAULT_VERSION="{target}"')
    edit(installer, r'(compare_versions "\$1" )' + re.escape(floor_version) + r"(?=\))",
         lambda match: match[1] + start)
    guidance = (r"from release " + VERSION + r" \(PROBE_404_VERSION=" + VERSION + r"\)"
                r"(?: or " + VERSION + r" \(PROBE_404_VERSION=" + VERSION + r"\))?")
    edit(installer, guidance, f"from release {start} (PROBE_404_VERSION={start})", 4)
    edit(storage, r"^const minimumUpgradeSchemaVersion = \d+$",
         f"const minimumUpgradeSchemaVersion = {floor_schema}")
    edit(storage, guidance, f"from release {start} (PROBE_404_VERSION={start})")

    edit("cmd/server/installer_contract_test.go", f'DEFAULT_VERSION="{re.escape(previous)}"',
         f'DEFAULT_VERSION="{target}"')
    metadata_test = "cmd/release-metadata/main_test.go"
    edit(metadata_test, r'(\{"' + re.escape(previous) + r'", 2\})(?=\})',
         lambda match: match[1] + f', {{"{target}", 2}}')
    for name, count in (("scripts/build-release-assets.sh", 1),
                        ("scripts/test-install-banner.sh", 1),
                        ("scripts/test-release-build-gates.sh", 7)):
        edit(name, re.escape(previous) + r"(?![0-9])", target, count)

    digits = "cmd/server/installer_version_digits_test.go"
    edit(digits, r'\}\{\{"' + VERSION + r'", "' + VERSION + r'", true, true\}, \{"' + VERSION +
         r'", "' + VERSION + r'", false, false\}, \{"' + VERSION + r'", "' + VERSION +
         r'", true, false\}\} \{',
         f'}}{{{{"{start}", "{target}", true, true}}, {{"{target}", "{start}", false, false}}, '
         f'{{"{target}", "{target}", true, false}}}} {{')
    edit(digits, r'(\$CURRENT" == )' + VERSION, lambda match: match[1] + start)
    edit(digits, r'("version":")' + VERSION, lambda match: match[1] + target)
    edit(digits, r'(\$PROBE_404_VERSION" == )' + VERSION, lambda match: match[1] + start)
    edit(digits, r'(row.to == ")' + VERSION, lambda match: match[1] + start)

    floor_test = "internal/storage/upgrade_floor_test.go"
    edit(floor_test, r'\[\]string\{"' + VERSION + r'"(?:, "' + VERSION + r'")*\}',
         f'[]string{{"{start}"}}')
    # The fixture owns the current/newer schema cases. Only the release's
    # supported upgrade floor and the version in its guidance change here.
    edit(floor_test, r"if version <= \d+ \{", f"if version <= {floor_schema - 1} {{")

    shell_test = "scripts/test-upgrade-floor.sh"
    edit(shell_test, r"^(for identity in v1\.0\.8 .+) unknown; do$",
         lambda match: match[1] + f" {floor_version} unknown; do"
         if floor_version != start and floor_version not in match[1].split() else match[0])
    # The old upper end may still have been supported by the outgoing installer.
    if start != "v1.3.0" and version_key(start) > version_key("v1.3.0"):
        rejected = re.search(r"^for identity in v1\.0\.8 .+ unknown; do$", read(shell_test), re.M)[0]
        if "v1.3.0" not in rejected.split():
            edit(shell_test, re.escape(rejected), rejected.replace(" unknown;", " v1.3.0 unknown;"))
    edit(shell_test, r"^for identity in " + VERSION + r" " + VERSION + r"; do$",
         f"for identity in {start} {target}; do")
    edit(shell_test, r'(\$\{identity\}" == )' + VERSION + r"( ]]; then sed -i 's/)" +
         VERSION + "/" + VERSION + "/'",
         lambda match: f"{match[1]}{start}{match[2]}{start}/{target}/'")
    edit(shell_test, r'(" == )' + VERSION + r"(?= \]\]$)", lambda match: match[1] + target)
    edit(shell_test, r'("version":")' + VERSION + r'(?=","dirty":true)', lambda match: match[1] + start)
    edit(shell_test, r"(grep -Fq 'PROBE_404_VERSION=)" + VERSION, lambda match: match[1] + start)

    description = (f"数据库 schema 保持 {target_schema}。" if floor_schema == target_schema
                   else f"安装器会将数据库 schema 从 {floor_schema} 迁移到 {target_schema}。")
    readme = "README.md"
    # Keep the outgoing release summary as history. Update active commands and links only.
    if not re.search(r"^> " + re.escape(previous) + r" Stable", read(readme), re.M):
        raise ValueError("README.md: outgoing release summary missing")
    edit(readme, r"^(> " + re.escape(previous) + r" Stable.*)$",
         lambda match: f"> {target} Stable：详见 [发布说明](RELEASE_NOTES_{target}.md)。\n" + match[0])
    upgrade = (f"> **本版支持从 {start} 升级**（schema {floor_schema}）。{description}更早版本会在改动任何"
               f"文件之前被安装器拒绝，请先查阅中间版本发布说明中的支持起点。版本窗口只由安装器保证，"
               f"手动替换二进制不保证。详情见 [{target} 发布说明](RELEASE_NOTES_{target}.md)“升级前必读”。")
    edit(readme, r"^> \*\*本版支持从 .+$", upgrade)
    edit(readme, r"^\*\*现有 Server 升级（支持 .+?确认目标 " + re.escape(previous) + r"。",
         f"**现有 Server 升级（支持 {start}）：**在 Server 主机运行上面的同一命令，确认目标 {target}。")
    # These sentences exist in two shapes: hand-written in a release whose preparation never ran, or
    # generated by the previous preparation. Accept both so the next release can be prepared again.
    edit(readme, r"(?:本版没有新增数据库迁移：.+?安装器在受保护备份之后执行迁移，并做完整性校验。"
                 r"|(?:安装器会将数据库 schema 从 \d+ 迁移到 \d+|数据库 schema 保持 \d+)。"
                 r"安装器在受保护备份之后执行必要的迁移，并做完整性校验。)",
         f"{description}安装器在受保护备份之后执行必要的迁移，并做完整性校验。")
    edit(readme, r"窗口外的 Server(?:（包括 " + VERSION + r"）| )或 schema \d+ 及更早的数据库",
         f"窗口外的 Server 或 schema {floor_schema - 1} 及更早的数据库")
    agent_old = "本版 Agent 没有功能变化，面板升级 Agent 可以晚点做。"
    agent_new = "Server 升级完成后，可在面板中升级 Agent。"
    if agent_old in read(readme):
        edit(readme, re.escape(agent_old), agent_new)
    elif agent_new not in read(readme):
        raise ValueError("README.md: Agent upgrade sentence missing")
    edit(readme, r"^the current " + VERSION + r" Server [\s\S]*?"
         r"Preserve the protected upgrade backup and do not directly\s+downgrade the migrated database\.",
         f"the current {target} Server supports upgrades from {start} (schema {floor_schema}).\n"
         "Preserve the protected upgrade backup and do not directly downgrade the migrated database.")
    # The lines generated above name the supported start, which equals the outgoing
    # version when the window starts at the previous release. Never retarget them.
    files[readme] = retarget_readme_lines(read(readme), previous, target, (
        f"本版支持从 {start} 升级", f"现有 Server 升级（支持 {start}）"))

    notes_name = f"RELEASE_NOTES_{target}.md"
    notes_path = root / notes_name
    header = (f"# 404-probe {target}\n\n## 升级前必读\n\n支持从 {start} 升级。\n\n"
              f"{description}请保留受保护的升级备份。版本窗口只由安装器保证，手动替换二进制不保证。\n")
    supplied = notes or (read(notes_name) if notes_path.is_file() else None)
    if supplied is not None:
        if "\r" in supplied or not supplied.startswith(f"# 404-probe {target}\n"):
            raise ValueError("Release notes must start with the release heading and use LF")
        if f"支持从 {start} 升级" not in supplied or "手动替换二进制不保证" not in supplied:
            raise ValueError("Release notes must describe the selected upgrade window")
    files[notes_name] = supplied if supplied is not None else header

    for name, content in files.items():
        path = root / name
        encoded = content.encode("utf-8")
        if not path.exists() or path.read_bytes() != encoded:
            path.write_bytes(encoded)
