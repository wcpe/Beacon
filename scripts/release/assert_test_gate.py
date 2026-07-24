#!/usr/bin/env python3
"""校验测试报告不存在跳过、失败、异常报告或空跑。"""

from __future__ import annotations

import argparse
import glob
import json
import os
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def write_summary(path: Path, summary: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def event_name(row: dict[str, Any]) -> str:
    package = row.get("Package") or "未知包"
    test = row.get("Test") or "包级事件"
    return f"{package}::{test}"


def check_go_json(
    path: Path,
    expected_tests: set[str],
    expected_packages: set[str] | None = None,
    *,
    summary_path: Path | None = None,
    dsn: str | None = None,
    test_exit_code: int = 0,
    command: str = "go test -json",
) -> dict[str, Any]:
    raw = path.read_text(encoding="utf-8")
    rows: list[dict[str, Any]] = []
    malformed_lines: list[int] = []
    for number, line in enumerate(raw.splitlines(), start=1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            malformed_lines.append(number)
            continue
        if not isinstance(value, dict):
            malformed_lines.append(number)
            continue
        rows.append(value)

    expected_packages = expected_packages or set()
    passed_tests = {row["Test"] for row in rows if row.get("Action") == "pass" and row.get("Test")}
    passed_packages = {row["Package"] for row in rows if row.get("Action") == "pass" and row.get("Package")}
    skipped = sorted({event_name(row) for row in rows if row.get("Action") == "skip"})
    failed = sorted({event_name(row) for row in rows if row.get("Action") in {"fail", "error"}})
    missing_tests = sorted(expected_tests - passed_tests)
    missing_packages = sorted(expected_packages - passed_packages)
    dsn_value = os.environ.get("BEACON_TEST_DSN", "") if dsn is None else dsn
    dsn_leaked = bool(dsn_value and dsn_value in raw)
    summary = {
        "command": command,
        "testExitCode": test_exit_code,
        "expectedTests": sorted(expected_tests),
        "passedExpectedTests": sorted(expected_tests & passed_tests),
        "missingTests": missing_tests,
        "expectedPackages": sorted(expected_packages),
        "passedExpectedPackages": sorted(expected_packages & passed_packages),
        "missingPackages": missing_packages,
        "skippedEvents": skipped,
        "skipCount": len(skipped),
        "failedEvents": failed,
        "malformedJsonLines": malformed_lines,
        "dsnInjected": bool(dsn_value),
        "dsnLeaked": dsn_leaked,
    }
    summary["ok"] = bool(rows) and test_exit_code == 0 and not any((
        malformed_lines, skipped, failed, missing_tests, missing_packages, dsn_leaked,
    )) and bool(passed_tests or passed_packages)
    output_path = summary_path or path.with_name(f"{path.stem}-summary.json")
    if dsn_leaked:
        path.write_text('{"Action":"error","Output":"检测到 DSN 泄露，原报告已销毁"}\n', encoding="utf-8")
    write_summary(output_path, summary)

    require(not malformed_lines, f"Go 测试报告存在异常 JSON 行：{malformed_lines}")
    require(not dsn_leaked, "Go 测试报告检测到 DSN 泄露")
    require(test_exit_code == 0, f"Go 测试命令退出码非 0：{test_exit_code}")
    require(not skipped, f"Go 测试存在跳过：{skipped}")
    require(not failed, f"Go 测试报告存在失败事件：{failed}")
    require(bool(passed_tests or passed_packages), "Go 测试报告没有通过用例或包")
    require(not missing_tests, f"Go 测试缺少预期通过用例：{missing_tests}")
    require(not missing_packages, f"Go 测试缺少预期通过包：{missing_packages}")
    return summary


def string_set(value: str | set[str] | list[str]) -> set[str]:
    if isinstance(value, str):
        return {value} if value else set()
    return set(value)


def check_junit(
    pattern: str,
    expected_classes: str | set[str] | list[str],
    expected_tests: set[str] | list[str] | None = None,
    *,
    summary_path: Path | None = None,
) -> dict[str, Any]:
    files = [Path(value) for value in glob.glob(pattern, recursive=True)]
    require(bool(files), f"没有匹配到 JUnit 报告：{pattern}")
    expected_class_set = string_set(expected_classes)
    expected_test_set = set(expected_tests or set())
    found_classes: set[str] = set()
    found_tests: set[str] = set()
    failures: list[str] = []
    skipped: list[str] = []
    case_count = 0
    suite_skipped = 0

    for path in files:
        root = ET.parse(path).getroot()
        for suite in root.iter("testsuite"):
            suite_skipped += int(suite.attrib.get("skipped", "0"))
            suite_skipped += int(suite.attrib.get("disabled", "0"))
        for case in root.iter("testcase"):
            case_count += 1
            class_name = case.attrib.get("classname", "")
            test_name = case.attrib.get("name", "")
            if class_name:
                found_classes.add(class_name)
            if test_name:
                found_tests.add(test_name)
            case_label = f"{class_name or '未知类'}::{test_name or '未知用例'}"
            if case.find("skipped") is not None:
                skipped.append(case_label)
            if case.find("failure") is not None or case.find("error") is not None:
                failures.append(case_label)

    missing_classes = sorted(expected_class_set - found_classes)
    missing_tests = sorted(expected_test_set - found_tests)
    summary = {
        "matchedFiles": [str(path) for path in files],
        "testCaseCount": case_count,
        "failureEvents": sorted(failures),
        "skippedEvents": sorted(skipped),
        "suiteSkippedCount": suite_skipped,
        "expectedClasses": sorted(expected_class_set),
        "missingClasses": missing_classes,
        "expectedTests": sorted(expected_test_set),
        "missingTests": missing_tests,
    }
    summary["ok"] = case_count > 0 and not any((failures, skipped, suite_skipped, missing_classes, missing_tests))
    if summary_path is not None:
        write_summary(summary_path, summary)

    require(case_count > 0, "JUnit 报告测试数为 0")
    require(not failures, f"JUnit 存在失败：{sorted(failures)}")
    require(not skipped and suite_skipped == 0, f"JUnit 存在跳过：用例 {sorted(skipped)}，包级 {suite_skipped}")
    require(not missing_classes, f"JUnit 未执行预期测试类：{missing_classes}")
    require(not missing_tests, f"JUnit 未执行预期测试：{missing_tests}")
    return summary


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="subcommand", required=True)
    go_json = commands.add_parser("go-json")
    go_json.add_argument("--input", required=True)
    go_json.add_argument("--expected", action="append", default=[])
    go_json.add_argument("--expected-package", action="append", default=[])
    go_json.add_argument("--summary")
    go_json.add_argument("--test-exit-code", type=int, default=0)
    go_json.add_argument("--command", dest="executed_command", default="go test -json")
    junit = commands.add_parser("junit")
    junit.add_argument("--glob", required=True)
    junit.add_argument("--expected-class", action="append", required=True)
    junit.add_argument("--expected-test", action="append", default=[])
    junit.add_argument("--summary")
    return root


def main() -> int:
    args = parser().parse_args()
    try:
        if args.subcommand == "go-json":
            check_go_json(
                Path(args.input),
                set(args.expected),
                set(args.expected_package),
                summary_path=Path(args.summary) if args.summary else None,
                test_exit_code=args.test_exit_code,
                command=args.executed_command,
            )
        else:
            check_junit(
                args.glob,
                set(args.expected_class),
                set(args.expected_test),
                summary_path=Path(args.summary) if args.summary else None,
            )
    except (OSError, ValueError, json.JSONDecodeError, ET.ParseError) as exc:
        print(f"测试门校验失败：{exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
