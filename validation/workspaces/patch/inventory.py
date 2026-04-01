def available_units(items):
    total = 0
    for item in items:
        total += item["stock"] - item["reserved"]
    return total


def summarize(items):
    return {
        "count": len(items),
        "available_units": available_units(items),
    }
