from .rules import normalize_findings


def build_summary(findings):
    normalized = normalize_findings(findings)
    highest = normalized[0]["severity"] if normalized else "none"
    return {
        "total": len(normalized),
        "highest_severity": highest,
        "titles": [item["title"] for item in normalized],
    }
