from app.report import build_summary


def test_build_summary_reports_highest_severity_first():
    findings = [
        {"title": "slow query", "severity": "medium"},
        {"title": "auth bypass", "severity": "high"},
        {"title": "formatting nit", "severity": "low"},
    ]
    summary = build_summary(findings)
    assert summary["highest_severity"] == "high"
    assert summary["titles"] == ["auth bypass", "slow query", "formatting nit"]
