import unittest

from run_experiments import summarize, validate


class ExperimentResultsTest(unittest.TestCase):
    def result(self):
        return summarize({
            "diagnostics": [],
            "status": {
                "incomplete": False,
                "units": {"discovered": 1, "analyzed": 1, "skipped": 0, "excluded_files": 0},
                "unsupported": {"targets": 2, "handed_off_obligations": 1, "unestablished_path_checks": 3},
            },
        })

    def test_no_warning_preserves_semantic_gaps(self):
        result = self.result()
        self.assertEqual(result["rules"], [])
        self.assertEqual(result["unsupported_targets"], 2)
        self.assertEqual(result["handed_off"], 1)
        self.assertEqual(result["unestablished_path_checks"], 3)

    def test_missing_warning_is_a_failure(self):
        self.assertTrue(validate(self.result(), {"rules": ["LL1001"]}))

    def test_duplicate_diagnostics_are_not_deduplicated(self):
        result = self.result()
        result["rules"] = ["LL1002", "LL1002"]
        self.assertTrue(validate(result, {"rules": ["LL1002"]}))

    def test_handoff_checked_without_diagnostics(self):
        self.assertEqual(validate(self.result(), {"rules": [], "handed_off": 1}), [])
        self.assertTrue(validate(self.result(), {"rules": [], "handed_off": 0}))

    def test_incomplete_unlabeled_case_is_a_failure(self):
        result = self.result()
        result["incomplete"] = True
        self.assertTrue(validate(result, None))

    def test_wrong_caller_is_a_failure(self):
        result = self.result()
        result.update(rules=["LL1001"], functions=["Good"])
        self.assertTrue(validate(result, {"rules": ["LL1001"], "functions": ["Bad"]}))


if __name__ == "__main__":
    unittest.main()
