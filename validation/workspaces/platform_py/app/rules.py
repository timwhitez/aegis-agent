SEVERITY_ORDER = ["low", "medium", "high"]


def normalize_findings(items):
    return sorted(items, key=lambda item: SEVERITY_ORDER.index(item["severity"]))
