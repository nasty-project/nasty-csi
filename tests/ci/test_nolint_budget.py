import unittest

from check_nolint_budget import count_suppressions, validate


class SuppressionBudgetTests(unittest.TestCase):
    def test_counts_named_directives(self):
        self.assertEqual(count_suppressions("//nolint:govet,gosec // reason\n// nolintlint is not a directive"), 2)

    def test_blanket_suppressions_fail(self):
        for text in ["//nolint // reason", "//nolint:all // reason"]:
            with self.assertRaises(ValueError):
                count_suppressions(text)

    def test_additions_and_unlowered_budget_fail(self):
        for actual, budget in [(3, 2), (1, 2)]:
            with self.assertRaises(ValueError):
                validate(actual, budget, 2)

    def test_baseline_cannot_be_raised(self):
        with self.assertRaises(ValueError):
            validate(3, 3, 2)
        validate(1, 1, 2)
        validate(2, 2, None)

    def test_invalid_budget_fails(self):
        for budget in [True, -1, "2"]:
            with self.assertRaises(ValueError):
                validate(2, budget)


if __name__ == "__main__":
    unittest.main()
