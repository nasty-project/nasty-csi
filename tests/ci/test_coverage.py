import unittest

from check_coverage import package_coverage, validate


class CoverageTests(unittest.TestCase):
    def test_weights_statements_not_blocks_or_execution_count(self):
        profile = "mode: set\nmodule/pkg/file.go:1.1,2.2 3 100\nmodule/pkg/other.go:2.1,3.1 1 0\n"
        self.assertEqual(package_coverage(profile), {"module/pkg": 75.0})

    def test_decrease_or_missing_package_fails(self):
        for current in [{"pkg": 74}, {"other": 100}, {}]:
            with self.assertRaises(ValueError):
                validate(current, {"pkg": 75})

    def test_noise_tolerance_and_improvement(self):
        validate({"pkg": 74.5}, {"pkg": 75})
        validate({"pkg": 80}, {"pkg": 75})
        with self.assertRaises(ValueError):
            validate({"pkg": 74.49}, {"pkg": 75})

    def test_empty_baseline_fails(self):
        with self.assertRaises(ValueError):
            validate({"pkg": 80}, {})
