from __future__ import annotations

import contextlib
import copy
import io
import os
import unittest
from unittest import mock

from tools import hfc_gpt56_admin_config as config_tool


class FakeAdminClient:
    def __init__(
        self,
        *,
        source: str,
        status: str = "active",
        mapped: bool = False,
        channel_status: str = "active",
        channel_group_ids: list[int] | None = None,
        fail_after_commit_path: str = "",
        rollback_fail_path: str = "",
    ) -> None:
        mapping = {model: model for model in config_tool.GPT56_TIERS} if mapped else {}
        self.token = ""
        self.calls: list[tuple[str, str]] = []
        self.fail_after_commit_path = fail_after_commit_path
        self.rollback_fail_path = rollback_fail_path
        self.failure_injected = False
        self.channel = {
            "id": 2,
            "name": "gpt",
            "status": channel_status,
            "group_ids": [3] if channel_group_ids is None else channel_group_ids,
            "billing_model_source": source,
        }
        self.accounts = {
            account_id: {
                "id": account_id,
                "platform": "openai",
                "type": "apikey",
                "status": status,
                "schedulable": status == "active",
                "group_ids": [3],
                "credentials": {
                    "api_key": f"sk-test-{account_id}",
                    "base_url": "https://upstream.example",
                    "model_mapping": copy.deepcopy(mapping),
                },
            }
            for account_id in config_tool.HFC_APPROVED_ACCOUNT_IDS
        }

    def get_data(self, path: str) -> dict[str, object]:
        if path == "/api/v1/admin/channels/2":
            return copy.deepcopy(self.channel)
        account_id = int(path.rsplit("/", 1)[1])
        return copy.deepcopy(self.accounts[account_id])

    def put_data(self, path: str, payload: dict[str, object]) -> dict[str, object]:
        self.calls.append(("PUT", path))
        if self.failure_injected and path == self.rollback_fail_path:
            raise RuntimeError("injected rollback failure")
        if path == "/api/v1/admin/channels/2":
            self.channel["billing_model_source"] = payload["billing_model_source"]
            result = copy.deepcopy(self.channel)
        else:
            account_id = int(path.rsplit("/", 1)[1])
            self.accounts[account_id]["credentials"] = copy.deepcopy(payload["credentials"])
            result = copy.deepcopy(self.accounts[account_id])
        if not self.failure_injected and path == self.fail_after_commit_path:
            self.failure_injected = True
            raise RuntimeError("injected timeout after commit")
        return result


class HFCGPT56AdminConfigTest(unittest.TestCase):
    def test_enable_preserves_credentials_and_unrelated_mappings(self) -> None:
        credentials = {
            "api_key": "sk-test-secret",
            "base_url": "https://upstream.example",
            "model_mapping": {"gpt-5.5": "gpt-5.5"},
        }

        updated = config_tool.merge_exact_mappings(credentials)

        self.assertIsNot(updated, credentials)
        self.assertEqual("sk-test-secret", updated["api_key"])
        self.assertEqual("gpt-5.5", updated["model_mapping"]["gpt-5.5"])
        for model in config_tool.GPT56_TIERS:
            self.assertEqual(model, updated["model_mapping"][model])
        self.assertNotIn("gpt-5.6-sol", credentials["model_mapping"])

    def test_enable_rejects_conflicting_tier_mapping(self) -> None:
        credentials = {
            "api_key": "sk-test-secret",
            "base_url": "https://upstream.example",
            "model_mapping": {"gpt-5.6-sol": "gpt-5.6-terra"},
        }

        with self.assertRaisesRegex(ValueError, "conflicting exact mapping"):
            config_tool.merge_exact_mappings(credentials)

    def test_disable_removes_only_exact_tier_mappings(self) -> None:
        credentials = {
            "api_key": "sk-test-secret",
            "base_url": "https://upstream.example",
            "model_mapping": {
                "gpt-5.5": "gpt-5.5",
                "gpt-5.6-sol": "gpt-5.6-sol",
                "gpt-5.6-terra": "gpt-5.6-terra",
                "gpt-5.6-luna": "gpt-5.6-luna",
            },
        }

        updated = config_tool.remove_exact_mappings(credentials)

        self.assertEqual({"gpt-5.5": "gpt-5.5"}, updated["model_mapping"])
        self.assertEqual("sk-test-secret", updated["api_key"])

    def test_admin_endpoint_must_be_loopback_http(self) -> None:
        self.assertEqual(
            "http://127.0.0.1:8080",
            config_tool.validate_admin_base_url("http://127.0.0.1:8080/"),
        )
        with self.assertRaisesRegex(ValueError, "loopback"):
            config_tool.validate_admin_base_url("https://api.handsfreeclub.com")

    def test_admin_auth_prefers_preissued_token_without_password_login(self) -> None:
        client = mock.Mock()
        args = mock.Mock(token_env="HFC_ADMIN_TOKEN", password_env="HFC_ADMIN_PASSWORD")

        with mock.patch.dict(os.environ, {"HFC_ADMIN_TOKEN": "signed-admin-token"}, clear=True):
            config_tool.configure_admin_auth(client, args)

        self.assertEqual("signed-admin-token", client.token)
        client.login.assert_not_called()

    def test_admin_auth_requires_token_or_password(self) -> None:
        client = mock.Mock()
        args = mock.Mock(token_env="HFC_ADMIN_TOKEN", password_env="HFC_ADMIN_PASSWORD")

        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "admin token or password"):
                config_tool.configure_admin_auth(client, args)

    def test_disable_accepts_error_account_for_emergency_rollback(self) -> None:
        account = {
            "id": 1682,
            "platform": "openai",
            "type": "apikey",
            "status": "error",
            "schedulable": False,
            "group_ids": [3],
            "credentials": {
                "api_key": "sk-test-secret",
                "base_url": "https://upstream.example",
                "model_mapping": {model: model for model in config_tool.GPT56_TIERS},
            },
        }

        credentials = config_tool.validate_account(
            account,
            1682,
            group_id=3,
            require_schedulable=False,
        )

        self.assertEqual("sk-test-secret", credentials["api_key"])

    def test_account_must_belong_to_target_group(self) -> None:
        account = {
            "id": 1682,
            "platform": "openai",
            "type": "apikey",
            "status": "active",
            "schedulable": True,
            "group_ids": [17],
            "credentials": {
                "api_key": "sk-test-secret",
                "base_url": "https://upstream.example",
                "model_mapping": {},
            },
        }

        with self.assertRaisesRegex(RuntimeError, "target group"):
            config_tool.validate_account(account, 1682, group_id=3, require_schedulable=True)

    def test_restore_exact_mappings_preserves_concurrent_unrelated_changes(self) -> None:
        original = {
            "api_key": "sk-old",
            "base_url": "https://upstream.example",
            "model_mapping": {"gpt-5.5": "gpt-5.5"},
        }
        current = {
            "api_key": "sk-rotated",
            "base_url": "https://upstream.example",
            "model_mapping": {
                "gpt-5.5": "gpt-5.5-new",
                **{model: model for model in config_tool.GPT56_TIERS},
            },
        }

        restored = config_tool.restore_exact_mappings(current, original)

        self.assertEqual("sk-rotated", restored["api_key"])
        self.assertEqual("gpt-5.5-new", restored["model_mapping"]["gpt-5.5"])
        for model in config_tool.GPT56_TIERS:
            self.assertNotIn(model, restored["model_mapping"])

    def test_redirect_handler_refuses_redirects(self) -> None:
        handler = config_tool.RejectRedirectHandler()

        self.assertIsNone(
            handler.redirect_request(
                mock.Mock(),
                mock.Mock(),
                302,
                "redirect refused",
                {},
                "https://attacker.example/",
            )
        )

    def test_admin_and_upstream_requests_ignore_environment_proxies(self) -> None:
        proxy_handlers = [
            handler
            for handler in config_tool.NO_REDIRECT_OPENER.handlers
            if isinstance(handler, config_tool.urllib.request.ProxyHandler)
        ]

        self.assertFalse(any(handler.proxies for handler in proxy_handlers))

    def test_only_approved_production_account_set_is_accepted(self) -> None:
        config_tool.validate_approved_account_ids([1682, 1684])
        with self.assertRaisesRegex(RuntimeError, "approved production account set"):
            config_tool.validate_approved_account_ids([1682])

    def test_only_approved_channel_and_group_are_accepted(self) -> None:
        config_tool.validate_approved_target(2, "gpt", 3)
        with self.assertRaisesRegex(RuntimeError, "approved production target"):
            config_tool.validate_approved_target(2, "gpt", 17)

    def test_requested_restore_billing_source_is_rejected_by_parser(self) -> None:
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                config_tool.build_parser().parse_args(
                    [
                        "--mode",
                        "disable",
                        "--account-id",
                        "1682",
                        "--restore-billing-source",
                        "requested",
                    ]
                )

    def test_enable_switches_billing_source_before_exposing_exact_mappings(self) -> None:
        fake = FakeAdminClient(source="channel_mapped")
        args = config_tool.build_parser().parse_args(
            [
                "--mode",
                "enable",
                "--account-id",
                "1682",
                "--account-id",
                "1684",
                "--execute",
            ]
        )
        args.account_id = sorted(set(args.account_id))

        with mock.patch.dict(os.environ, {"HFC_ADMIN_TOKEN": "signed-admin-token"}, clear=True), mock.patch.object(
            config_tool,
            "JSONClient",
            return_value=fake,
        ), mock.patch.object(config_tool, "probe_upstream_models", return_value=list(config_tool.GPT56_TIERS)):
            self.assertEqual(0, config_tool.run(args))

        self.assertEqual(("PUT", "/api/v1/admin/channels/2"), fake.calls[0])
        self.assertEqual("upstream", fake.channel["billing_model_source"])
        for account in fake.accounts.values():
            self.assertEqual(
                {model: model for model in config_tool.GPT56_TIERS},
                config_tool.exact_mapping_snapshot(account["credentials"]),
            )

    def test_disable_removes_mappings_before_restoring_channel_source(self) -> None:
        fake = FakeAdminClient(
            source="upstream",
            status="error",
            mapped=True,
            channel_status="disabled",
            channel_group_ids=[],
        )
        args = config_tool.build_parser().parse_args(
            [
                "--mode",
                "disable",
                "--account-id",
                "1682",
                "--account-id",
                "1684",
                "--execute",
            ]
        )
        args.account_id = sorted(set(args.account_id))

        with mock.patch.dict(os.environ, {"HFC_ADMIN_TOKEN": "signed-admin-token"}, clear=True), mock.patch.object(
            config_tool,
            "JSONClient",
            return_value=fake,
        ):
            self.assertEqual(0, config_tool.run(args))

        self.assertEqual(("PUT", "/api/v1/admin/channels/2"), fake.calls[-1])
        self.assertEqual("channel_mapped", fake.channel["billing_model_source"])
        for account in fake.accounts.values():
            self.assertEqual({}, config_tool.exact_mapping_snapshot(account["credentials"]))

    def test_committed_timeout_is_fully_rolled_back_and_verified(self) -> None:
        fake = FakeAdminClient(
            source="channel_mapped",
            fail_after_commit_path="/api/v1/admin/accounts/1684",
        )
        args = config_tool.build_parser().parse_args(
            [
                "--mode",
                "enable",
                "--account-id",
                "1682",
                "--account-id",
                "1684",
                "--execute",
            ]
        )
        args.account_id = sorted(set(args.account_id))

        with mock.patch.dict(os.environ, {"HFC_ADMIN_TOKEN": "signed-admin-token"}, clear=True), mock.patch.object(
            config_tool,
            "JSONClient",
            return_value=fake,
        ), mock.patch.object(config_tool, "probe_upstream_models", return_value=list(config_tool.GPT56_TIERS)):
            with self.assertRaisesRegex(RuntimeError, "rollback completed and verified"):
                config_tool.run(args)

        self.assertEqual("channel_mapped", fake.channel["billing_model_source"])
        for account in fake.accounts.values():
            self.assertEqual({}, config_tool.exact_mapping_snapshot(account["credentials"]))

    def test_rollback_failure_reports_only_object_identity(self) -> None:
        fake = FakeAdminClient(
            source="channel_mapped",
            fail_after_commit_path="/api/v1/admin/accounts/1684",
            rollback_fail_path="/api/v1/admin/accounts/1682",
        )
        args = config_tool.build_parser().parse_args(
            [
                "--mode",
                "enable",
                "--account-id",
                "1682",
                "--account-id",
                "1684",
                "--execute",
            ]
        )
        args.account_id = sorted(set(args.account_id))

        with mock.patch.dict(os.environ, {"HFC_ADMIN_TOKEN": "signed-admin-token"}, clear=True), mock.patch.object(
            config_tool,
            "JSONClient",
            return_value=fake,
        ), mock.patch.object(config_tool, "probe_upstream_models", return_value=list(config_tool.GPT56_TIERS)):
            with self.assertRaisesRegex(RuntimeError, "account:1682") as raised:
                config_tool.run(args)

        self.assertNotIn("sk-test", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
