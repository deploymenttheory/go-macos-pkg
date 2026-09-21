import unittest

from check_diff_coverage import changed_lines, measure, production, read_profile


class DiffCoverageTests(unittest.TestCase):
    def test_insert_replace_and_delete(self):
        diff = "@@ -1 +1,2 @@\n@@ -4,2 +5 @@\n@@ -9 +9,0 @@"
        self.assertEqual(changed_lines(diff), {1, 2, 5})

    def test_multiline_block_counted_once(self):
        blocks = {(1, 1, 5, 2, 3): True, (8, 1, 9, 2, 1): False}
        self.assertEqual(measure(blocks, {2, 3, 8}), (3, 4, ["8-9"]))
        self.assertEqual(measure(blocks, {6}), (0, 0, []))

    def test_duplicate_profile_records_and_other_modules(self):
        profile = "mode: atomic\nm/pkg/a.go:1.1,2.2 2 0\nm/pkg/a.go:1.1,2.2 2 1\nother/a.go:1.1,2.2 2 1"
        blocks = read_profile(profile, "m")
        self.assertEqual(dict(blocks), {"pkg/a.go": {(1, 1, 2, 2, 2): True}})

    def test_invalid_profile(self):
        with self.assertRaises(ValueError):
            read_profile("not a profile", "m")

    def test_production_scope(self):
        self.assertTrue(production("pkg/pbzx/pbzx.go"))
        for path in ("pkg/pbzx/a_test.go", "scripts/check.go", "README.md"):
            self.assertFalse(production(path))


if __name__ == "__main__":
    unittest.main()
