from __future__ import annotations

import contextlib
import hashlib
import io
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from tools import secret_scan


class SecretScanTest(unittest.TestCase):
    def write_fixture(self, value: str) -> tuple[tempfile.TemporaryDirectory[str], Path, Path]:
        tmp = tempfile.TemporaryDirectory()
        root = Path(tmp.name)
        path = root / "fixture.txt"
        path.write_text(f"credential={value}\n", encoding="utf-8")
        return tmp, root, path

    def test_real_google_oauth_shape_is_reported_without_value(self) -> None:
        value = "GOCSPX-" + "A" * 24
        tmp, root, path = self.write_fixture(value)
        self.addCleanup(tmp.cleanup)

        findings = secret_scan.scan_file(path, root)

        self.assertEqual(["fixture.txt:1 (google_oauth_client_secret)"], findings)
        self.assertNotIn(value, "\n".join(findings))

    def test_placeholder_is_ignored(self) -> None:
        tmp, root, path = self.write_fixture("GOCSPX-your-client-secret")
        self.addCleanup(tmp.cleanup)

        self.assertEqual([], secret_scan.scan_file(path, root))

    def test_exact_public_credential_digest_can_be_allowed(self) -> None:
        value = "GOCSPX-" + "B" * 24
        digest = hashlib.sha256(value.encode()).hexdigest()
        tmp, root, path = self.write_fixture(value)
        self.addCleanup(tmp.cleanup)

        with mock.patch.object(
            secret_scan,
            "PUBLIC_CREDENTIAL_SHA256_ALLOWLIST",
            {digest: "documented public OAuth client credential"},
        ):
            self.assertEqual([], secret_scan.scan_file(path, root))

    def test_allowlist_is_exact_not_path_wide(self) -> None:
        allowed = "GOCSPX-" + "C" * 24
        other = "GOCSPX-" + "D" * 24
        digest = hashlib.sha256(allowed.encode()).hexdigest()
        tmp, root, path = self.write_fixture(other)
        self.addCleanup(tmp.cleanup)

        with mock.patch.object(
            secret_scan,
            "PUBLIC_CREDENTIAL_SHA256_ALLOWLIST",
            {digest: "documented public OAuth client credential"},
        ):
            self.assertEqual(
                ["fixture.txt:1 (google_oauth_client_secret)"],
                secret_scan.scan_file(path, root),
            )

    def test_main_never_echoes_detected_value(self) -> None:
        value = "AIza" + "E" * 35
        tmp, root, _path = self.write_fixture(value)
        self.addCleanup(tmp.cleanup)
        stderr = io.StringIO()

        with contextlib.redirect_stderr(stderr):
            result = secret_scan.main(["--repo-root", str(root)])

        self.assertEqual(1, result)
        self.assertNotIn(value, stderr.getvalue())
        self.assertIn("google_api_key", stderr.getvalue())

    def test_git_inventory_includes_untracked_nonignored_files(self) -> None:
        with mock.patch.object(
            secret_scan.subprocess,
            "check_output",
            return_value=b"tracked.txt\0new-tool.py\0",
        ) as check_output, mock.patch.object(Path, "is_file", return_value=True), mock.patch.object(
            Path,
            "is_symlink",
            return_value=False,
        ):
            files = secret_scan.iter_git_files(Path("/repo"))

        self.assertEqual([Path("/repo/tracked.txt"), Path("/repo/new-tool.py")], files)
        self.assertEqual(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            check_output.call_args.args[0],
        )


if __name__ == "__main__":
    unittest.main()
