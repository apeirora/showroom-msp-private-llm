#!/usr/bin/env python3
"""Set the operator's packaged default image, rejecting unexpected YAML shape.

This is deliberately a narrow edit to the repository-owned image block. It does
not implement a general YAML parser or change nested portal image settings.
"""
import argparse
from pathlib import Path
import re


VERSION = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
)
IMAGE_KEY = re.compile(r"^(?:image|'image'|\"image\")\s*:")
IMAGE_HEADER = re.compile(r"image:[ ]*(?:#.*)?(?:\r?\n)?")
CHILD = re.compile(r"  (repository|tag|pullPolicy):[ ]+([^\r\n]+)(?:\r?\n)?")
TAG = re.compile(
    r"  tag:[ ]+(?:'([^']+)'|\"([^\"]+)\"|([^\s#]+))([ ]*(?:#.*)?)(\r?\n)?"
)


def validate_version(version):
    match = VERSION.fullmatch(version)
    if match is None or len(version) > 128:
        raise ValueError("image version must be SemVer without build metadata or a leading v")
    if match[4] and any(
        item.isdigit() and len(item) > 1 and item.startswith("0")
        for item in match[4].split(".")
    ):
        raise ValueError("numeric prerelease identifiers must not have leading zeros")


def release_values(text, version):
    validate_version(version)
    lines = text.splitlines(keepends=True)
    roots = [i for i, line in enumerate(lines) if IMAGE_KEY.match(line)]
    if len(roots) != 1 or IMAGE_HEADER.fullmatch(lines[roots[0]]) is None:
        raise ValueError("expected exactly one literal root image block")
    start = roots[0] + 1
    end = len(lines)
    for i in range(start, len(lines)):
        if lines[i].strip() and not lines[i].lstrip().startswith("#") and not lines[i].startswith((" ", "\t")):
            end = i
            break
    fields = {}
    for i in range(start, end):
        if not lines[i].strip() or lines[i].lstrip().startswith("#"):
            continue
        child = CHILD.fullmatch(lines[i])
        if child is None or child[1] in fields:
            raise ValueError("operator image block changed; review the release edit")
        fields[child[1]] = i
    if set(fields) != {"repository", "tag", "pullPolicy"}:
        raise ValueError("operator image block requires repository, tag and pullPolicy")
    index = fields["tag"]
    tag = TAG.fullmatch(lines[index])
    if tag is None:
        raise ValueError("expected one scalar operator image tag")
    validate_version(next(value for value in tag.groups()[:3] if value is not None))
    lines[index] = f'  tag: "{version}"{tag[4]}{tag[5] or ""}'
    return "".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("values", type=Path)
    parser.add_argument("version")
    args = parser.parse_args()
    try:
        before = args.values.read_text()
        after = release_values(before, args.version)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    args.values.write_text(after)


if __name__ == "__main__":
    main()
