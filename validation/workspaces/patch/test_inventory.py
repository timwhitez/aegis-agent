import unittest

from inventory import available_units, summarize


class InventoryTests(unittest.TestCase):
    def test_available_units_uses_stock_minus_reserved(self):
        items = [
            {"sku": "A-1", "stock": 8, "reserved": 3},
            {"sku": "B-2", "stock": 5, "reserved": 1},
        ]
        self.assertEqual(available_units(items), 9)

    def test_summarize_reports_count_and_available_units(self):
        items = [
            {"sku": "A-1", "stock": 8, "reserved": 3},
            {"sku": "B-2", "stock": 5, "reserved": 1},
            {"sku": "C-3", "stock": 4, "reserved": 0},
        ]
        self.assertEqual(
            summarize(items),
            {
                "count": 3,
                "available_units": 13,
            },
        )


if __name__ == "__main__":
    unittest.main()
