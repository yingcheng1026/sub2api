#!/usr/bin/env python3
"""Safely enable or disable HFC GPT-5.6 exact-tier production config.

The tool talks only to a loopback admin API, accepts a pre-issued admin JWT or
an admin password from an environment variable, verifies upstream model
catalogs before enabling, and never prints credentials or response bodies.
"""

from __future__ import annotations

import argparse
import copy
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Sequence


GPT56_TIERS = ("gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna")
HFC_APPROVED_ACCOUNT_IDS = frozenset({1682, 1684})
HFC_APPROVED_TARGET = (2, "gpt", 3)
SAFE_BILLING_SOURCES = frozenset({"upstream", "channel_mapped"})


class RejectRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self,
        req: urllib.request.Request,
        fp: Any,
        code: int,
        msg: str,
        headers: Any,
        newurl: str,
    ) -> None:
        return None


NO_REDIRECT_OPENER = urllib.request.build_opener(
    urllib.request.ProxyHandler({}),
    RejectRedirectHandler(),
)


def validate_admin_base_url(value: str) -> str:
    normalized = value.rstrip("/")
    parsed = urllib.parse.urlsplit(normalized)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost"}:
        raise ValueError("admin base URL must use loopback HTTP")
    if parsed.path or parsed.query or parsed.fragment:
        raise ValueError("admin base URL must be an origin without a path")
    return normalized


def clone_credentials(credentials: dict[str, Any]) -> dict[str, Any]:
    return copy.deepcopy(credentials)


def validate_credentials(credentials: dict[str, Any]) -> None:
    if not isinstance(credentials, dict):
        raise ValueError("credentials must be an object")
    if not isinstance(credentials.get("api_key"), str) or not credentials["api_key"].strip():
        raise ValueError("account credentials are missing api_key")
    base_url = credentials.get("base_url")
    if not isinstance(base_url, str) or not base_url.strip():
        raise ValueError("account credentials are missing base_url")
    parsed = urllib.parse.urlsplit(base_url)
    if parsed.scheme != "https" or not parsed.hostname:
        raise ValueError("upstream base_url must use HTTPS")
    mapping = credentials.get("model_mapping", {})
    if not isinstance(mapping, dict):
        raise ValueError("model_mapping must be an object")


def merge_exact_mappings(credentials: dict[str, Any]) -> dict[str, Any]:
    validate_credentials(credentials)
    updated = clone_credentials(credentials)
    mapping = copy.deepcopy(updated.get("model_mapping", {}))
    for model in GPT56_TIERS:
        existing = mapping.get(model)
        if existing is not None and existing != model:
            raise ValueError(f"conflicting exact mapping for {model}")
        mapping[model] = model
    updated["model_mapping"] = mapping
    return updated


def remove_exact_mappings(credentials: dict[str, Any]) -> dict[str, Any]:
    validate_credentials(credentials)
    updated = clone_credentials(credentials)
    mapping = copy.deepcopy(updated.get("model_mapping", {}))
    for model in GPT56_TIERS:
        existing = mapping.get(model)
        if existing is not None and existing != model:
            raise ValueError(f"refusing to remove conflicting mapping for {model}")
        mapping.pop(model, None)
    updated["model_mapping"] = mapping
    return updated


def restore_exact_mappings(
    current: dict[str, Any],
    original: dict[str, Any],
) -> dict[str, Any]:
    validate_credentials(current)
    validate_credentials(original)
    restored = clone_credentials(current)
    current_mapping = copy.deepcopy(restored.get("model_mapping", {}))
    original_mapping = original.get("model_mapping", {})
    for model in GPT56_TIERS:
        if model in original_mapping:
            current_mapping[model] = original_mapping[model]
        else:
            current_mapping.pop(model, None)
    restored["model_mapping"] = current_mapping
    return restored


def validate_approved_account_ids(account_ids: Sequence[int]) -> None:
    if set(account_ids) != HFC_APPROVED_ACCOUNT_IDS:
        raise RuntimeError("account IDs do not match the approved production account set")


def validate_approved_target(channel_id: int, channel_name: str, group_id: int) -> None:
    if (channel_id, channel_name, group_id) != HFC_APPROVED_TARGET:
        raise RuntimeError("channel and group do not match the approved production target")


class JSONClient:
    def __init__(self, base_url: str, timeout: float = 15.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.token = ""
        self.opener = NO_REDIRECT_OPENER

    def request(self, method: str, path: str, payload: Any | None = None) -> Any:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        headers = {"Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers=headers,
        )
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                raw = response.read(4_000_000)
        except urllib.error.HTTPError as exc:
            exc.read(4096)
            raise RuntimeError(f"admin request failed: {method} {path} HTTP {exc.code}") from None
        except Exception as exc:
            raise RuntimeError(f"admin request failed: {method} {path}: {type(exc).__name__}") from None
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            raise RuntimeError(f"admin request returned invalid JSON: {method} {path}") from None

    def login(self, email: str, password: str) -> None:
        response = self.request("POST", "/api/v1/auth/login", {"email": email, "password": password})
        token = response.get("data", {}).get("access_token") if isinstance(response, dict) else None
        if not isinstance(token, str) or not token:
            raise RuntimeError("admin login did not return an access token")
        self.token = token

    def get_data(self, path: str) -> dict[str, Any]:
        response = self.request("GET", path)
        data = response.get("data") if isinstance(response, dict) else None
        if not isinstance(data, dict):
            raise RuntimeError(f"admin response data is not an object: GET {path}")
        return data

    def put_data(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        response = self.request("PUT", path, payload)
        data = response.get("data") if isinstance(response, dict) else None
        if not isinstance(data, dict):
            raise RuntimeError(f"admin response data is not an object: PUT {path}")
        return data


def configure_admin_auth(client: JSONClient, args: argparse.Namespace) -> None:
    token = os.environ.get(args.token_env, "").strip()
    if token:
        client.token = token
        return
    password = os.environ.get(args.password_env, "")
    if not password:
        raise RuntimeError(
            f"missing admin token or password environment variable: "
            f"{args.token_env} or {args.password_env}"
        )
    client.login(args.admin_email, password)


def probe_upstream_models(credentials: dict[str, Any], timeout: float) -> list[str]:
    validate_credentials(credentials)
    base_url = str(credentials["base_url"]).rstrip("/")
    models_url = base_url + ("/models" if base_url.endswith("/v1") else "/v1/models")
    request = urllib.request.Request(
        models_url,
        headers={
            "Authorization": f"Bearer {credentials['api_key']}",
            "Accept": "application/json",
        },
    )
    try:
        with NO_REDIRECT_OPENER.open(request, timeout=timeout) as response:
            raw = response.read(4_000_000)
    except urllib.error.HTTPError as exc:
        exc.read(4096)
        raise RuntimeError(f"upstream model probe failed with HTTP {exc.code}") from None
    except Exception as exc:
        raise RuntimeError(f"upstream model probe failed: {type(exc).__name__}") from None
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        raise RuntimeError("upstream model probe returned invalid JSON") from None
    items = payload.get("data", []) if isinstance(payload, dict) else []
    model_ids = sorted(
        {
            str(item.get("id", ""))
            for item in items
            if isinstance(item, dict) and str(item.get("id", "")) in GPT56_TIERS
        }
    )
    missing = sorted(set(GPT56_TIERS) - set(model_ids))
    if missing:
        raise RuntimeError("upstream model probe is missing required exact GPT-5.6 tiers")
    return model_ids


def validate_account(
    account: dict[str, Any],
    account_id: int,
    *,
    group_id: int,
    require_schedulable: bool,
    require_target_binding: bool = True,
) -> dict[str, Any]:
    if account.get("id") != account_id:
        raise RuntimeError(f"account identity mismatch for {account_id}")
    if account.get("platform") != "openai" or account.get("type") != "apikey":
        raise RuntimeError(f"account {account_id} is not an OpenAI API-key account")
    if require_target_binding and group_id not in account.get("group_ids", []):
        raise RuntimeError(f"account {account_id} is not bound to the target group")
    if require_schedulable and (
        account.get("status") != "active" or account.get("schedulable") is not True
    ):
        raise RuntimeError(f"account {account_id} is not active and schedulable")
    credentials = account.get("credentials")
    if not isinstance(credentials, dict):
        raise RuntimeError(f"account {account_id} credentials are unavailable")
    validate_credentials(credentials)
    return credentials


def validate_channel(
    channel: dict[str, Any],
    channel_id: int,
    group_id: int,
    name: str,
    *,
    require_available: bool,
) -> None:
    if channel.get("id") != channel_id:
        raise RuntimeError("channel identity mismatch")
    if channel.get("name") != name:
        raise RuntimeError("channel is not the expected GPT channel")
    if require_available and channel.get("status") != "active":
        raise RuntimeError("channel is not the expected active GPT channel")
    if require_available and group_id not in channel.get("group_ids", []):
        raise RuntimeError(f"channel {channel_id} is not bound to group {group_id}")


def exact_mapping_snapshot(credentials: dict[str, Any]) -> dict[str, Any]:
    mapping = credentials.get("model_mapping", {})
    return {model: mapping[model] for model in GPT56_TIERS if model in mapping}


def get_account_credentials(
    client: JSONClient,
    account_id: int,
    args: argparse.Namespace,
    *,
    require_schedulable: bool,
    require_target_binding: bool | None = None,
) -> dict[str, Any]:
    if require_target_binding is None:
        require_target_binding = args.mode == "enable"
    account = client.get_data(f"/api/v1/admin/accounts/{account_id}")
    return validate_account(
        account,
        account_id,
        group_id=args.group_id,
        require_schedulable=require_schedulable,
        require_target_binding=require_target_binding,
    )


def update_account(
    client: JSONClient,
    account_id: int,
    args: argparse.Namespace,
    original: dict[str, Any],
) -> None:
    current = get_account_credentials(
        client,
        account_id,
        args,
        require_schedulable=args.mode == "enable",
    )
    if current.get("api_key") != original.get("api_key") or current.get("base_url") != original.get("base_url"):
        raise RuntimeError(f"account {account_id} credentials changed after preflight")
    desired = merge_exact_mappings(current) if args.mode == "enable" else remove_exact_mappings(current)
    client.put_data(f"/api/v1/admin/accounts/{account_id}", {"credentials": desired})


def verify_target_state(
    client: JSONClient,
    args: argparse.Namespace,
    target_source: str,
) -> None:
    expected = {model: model for model in GPT56_TIERS} if args.mode == "enable" else {}
    for account_id in args.account_id:
        credentials = get_account_credentials(
            client,
            account_id,
            args,
            require_schedulable=args.mode == "enable",
        )
        if exact_mapping_snapshot(credentials) != expected:
            raise RuntimeError(f"account {account_id} exact mapping verification failed")
    channel = client.get_data(f"/api/v1/admin/channels/{args.channel_id}")
    validate_channel(
        channel,
        args.channel_id,
        args.group_id,
        args.channel_name,
        require_available=args.mode == "enable",
    )
    if channel.get("billing_model_source") != target_source:
        raise RuntimeError("channel billing model source verification failed")


def restore_accounts(
    client: JSONClient,
    args: argparse.Namespace,
    originals: dict[int, dict[str, Any]],
    failures: list[str],
) -> None:
    for account_id in reversed(args.account_id):
        try:
            current = get_account_credentials(
                client,
                account_id,
                args,
                require_schedulable=False,
                require_target_binding=False,
            )
            restored = restore_exact_mappings(current, originals[account_id])
            client.put_data(f"/api/v1/admin/accounts/{account_id}", {"credentials": restored})
            verified = get_account_credentials(
                client,
                account_id,
                args,
                require_schedulable=False,
                require_target_binding=False,
            )
            if exact_mapping_snapshot(verified) != exact_mapping_snapshot(originals[account_id]):
                raise RuntimeError("exact mapping state mismatch")
        except Exception:
            failures.append(f"account:{account_id}")


def restore_channel(
    client: JSONClient,
    args: argparse.Namespace,
    original_source: str,
    failures: list[str],
) -> None:
    try:
        client.put_data(
            f"/api/v1/admin/channels/{args.channel_id}",
            {"billing_model_source": original_source},
        )
        channel = client.get_data(f"/api/v1/admin/channels/{args.channel_id}")
        if channel.get("billing_model_source") != original_source:
            raise RuntimeError("billing source mismatch")
    except Exception:
        failures.append(f"channel:{args.channel_id}")


def rollback_configuration(
    client: JSONClient,
    args: argparse.Namespace,
    originals: dict[int, dict[str, Any]],
    original_source: str,
) -> list[str]:
    failures: list[str] = []
    if args.mode == "enable":
        restore_accounts(client, args, originals, failures)
        restore_channel(client, args, original_source, failures)
    else:
        restore_channel(client, args, original_source, failures)
        restore_accounts(client, args, originals, failures)
    return failures


def run(args: argparse.Namespace) -> int:
    base_url = validate_admin_base_url(args.admin_base_url)
    validate_approved_account_ids(args.account_id)
    validate_approved_target(args.channel_id, args.channel_name, args.group_id)
    client = JSONClient(base_url, args.timeout)
    configure_admin_auth(client, args)
    channel = client.get_data(f"/api/v1/admin/channels/{args.channel_id}")
    validate_channel(
        channel,
        args.channel_id,
        args.group_id,
        args.channel_name,
        require_available=args.mode == "enable",
    )

    originals: dict[int, dict[str, Any]] = {}
    for account_id in args.account_id:
        credentials = get_account_credentials(
            client,
            account_id,
            args,
            require_schedulable=args.mode == "enable",
        )
        originals[account_id] = clone_credentials(credentials)
        if args.mode == "enable":
            probe_upstream_models(credentials, args.timeout)

    target_source = "upstream" if args.mode == "enable" else args.restore_billing_source
    plan = {
        "mode": args.mode,
        "execute": bool(args.execute),
        "group_id": args.group_id,
        "channel_id": args.channel_id,
        "target_billing_model_source": target_source,
        "account_ids": args.account_id,
        "verified_tiers": GPT56_TIERS if args.mode == "enable" else (),
    }
    if not args.execute:
        print(json.dumps(plan, ensure_ascii=False))
        return 0

    original_source = str(channel.get("billing_model_source") or "channel_mapped")
    if original_source not in SAFE_BILLING_SOURCES:
        raise RuntimeError("current channel billing source is unsafe for GPT production")
    try:
        if args.mode == "enable":
            client.put_data(
                f"/api/v1/admin/channels/{args.channel_id}",
                {"billing_model_source": target_source},
            )
        for account_id in args.account_id:
            update_account(client, account_id, args, originals[account_id])
        if args.mode == "disable":
            client.put_data(
                f"/api/v1/admin/channels/{args.channel_id}",
                {"billing_model_source": target_source},
            )
        verify_target_state(client, args, target_source)
    except Exception as apply_error:
        rollback_failures = rollback_configuration(client, args, originals, original_source)
        if rollback_failures:
            failed = ",".join(rollback_failures)
            raise RuntimeError(f"apply failed and rollback verification failed for {failed}") from apply_error
        raise RuntimeError("apply failed; rollback completed and verified") from apply_error

    plan["status"] = "applied_and_verified"
    print(json.dumps(plan, ensure_ascii=False))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("enable", "disable"), required=True)
    parser.add_argument("--account-id", action="append", type=int, required=True)
    parser.add_argument("--channel-id", type=int, default=2)
    parser.add_argument("--channel-name", default="gpt")
    parser.add_argument("--group-id", type=int, default=3)
    parser.add_argument("--admin-base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--admin-email", default="admin@relay.local")
    parser.add_argument("--token-env", default="HFC_ADMIN_TOKEN")
    parser.add_argument("--password-env", default="HFC_ADMIN_PASSWORD")
    parser.add_argument(
        "--restore-billing-source",
        choices=sorted(SAFE_BILLING_SOURCES),
        default="channel_mapped",
    )
    parser.add_argument("--timeout", type=float, default=15.0)
    parser.add_argument("--execute", action="store_true")
    return parser


def main(argv: Sequence[str]) -> int:
    try:
        args = build_parser().parse_args(argv)
        args.account_id = sorted(set(args.account_id))
        return run(args)
    except Exception as exc:
        sys.stderr.write(f"GPT-5.6 admin config FAILED: {exc}\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
