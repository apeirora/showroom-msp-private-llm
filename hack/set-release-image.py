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
SCALAR = re.compile(
    r"  (repository|tag):[ ]+(?:'([^']+)'|\"([^\"]+)\"|([^\s#]+))([ ]*(?:[ ]#.*)?)(\r?\n)?"
)
REPOSITORY = re.compile(r"ghcr\.io/[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?/private-llm-controller")


def validate_version(version):
    match = VERSION.fullmatch(version)
    if match is None or len(version) > 128:
        raise ValueError("image version must be SemVer without build metadata or a leading v")
    if match[4] and any(
        item.isdigit() and len(item) > 1 and item.startswith("0")
        for item in match[4].split(".")
    ):
        raise ValueError("numeric prerelease identifiers must not have leading zeros")


def validate_repository(repository):
    if REPOSITORY.fullmatch(repository) is None:
        raise ValueError("image repository must be ghcr.io/<lowercase GitHub owner>/private-llm-controller")


def release_values(text, version, repository):
    validate_version(version)
    validate_repository(repository)
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
    for field, replacement, validate in [
        ("repository", repository, validate_repository),
        ("tag", version, validate_version),
    ]:
        index = fields[field]
        scalar = SCALAR.fullmatch(lines[index])
        if scalar is None:
            raise ValueError(f"expected one scalar operator image {field}")
        validate(next(value for value in scalar.groups()[1:4] if value is not None))
        lines[index] = f'  {field}: "{replacement}"{scalar[5]}{scalar[6] or ""}'
    return "".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("values", type=Path)
    parser.add_argument("version")
    parser.add_argument("--repository", required=True)
    args = parser.parse_args()
    try:
        before = args.values.read_text()
        after = release_values(before, args.version, args.repository)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    args.values.write_text(after)


if __name__ == "__main__":
    main()
