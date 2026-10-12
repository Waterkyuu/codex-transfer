#!/usr/bin/env python3
"""Verify translated command examples and repository-local README assets."""

from pathlib import Path
import re


# check_readmes validates content references without contacting external services.
def check_readmes(root):
    examples = []
    for name, heading in (("README.md", "Install"), ("README.zh-CN.md", "安装")):
        document = root / name
        content = document.read_text(encoding="utf-8")
        assert content.split("## ", 1)[1].startswith(heading), f"{name}: installation must be first"
        examples.append(re.findall(r"```\w*\n(.*?)```", content, re.S))
        links = re.findall(r"\]\((\./[^)#\s]+)", content)
        images = re.findall(r'(?:src|srcset)="(\./[^\"]+)"', content)
        for reference in links + images:
            path = document.parent / reference
            assert path.is_file(), f"{name}: missing local reference {reference}"
            if reference.endswith(".png"):
                header = path.read_bytes()[:33]
                assert header[:8] == b"\x89PNG\r\n\x1a\n", f"Invalid PNG: {reference}"
                assert header[25] in (4, 6), f"PNG has no alpha channel: {reference}"
    assert examples[0] == examples[1], "Translated README command examples differ"
    print("README language examples, guide links, and transparent PNG references validated.")


if __name__ == "__main__":
    check_readmes(Path(__file__).resolve().parent.parent)
