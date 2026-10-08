"""Validate documented routes, limits and pagination. Requires PyYAML==6.0.3."""
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[1]
spec = yaml.safe_load((ROOT / "api/openapi.yaml").read_text(encoding="utf-8"))
assert spec["openapi"].startswith("3.1.")


def validate_refs(value):
    if isinstance(value, dict):
        assert "" not in value, "empty YAML key is not an OpenAPI property"
        if "$ref" in value:
            ref = value["$ref"]
            assert ref.startswith("#/"), f"unexpected external reference: {ref}"
            resolved = spec
            for part in ref[2:].split("/"):
                resolved = resolved[part.replace("~1", "/").replace("~0", "~")]
        for child in value.values():
            validate_refs(child)
    elif isinstance(value, list):
        for child in value:
            validate_refs(child)


validate_refs(spec)
for path, item in spec["paths"].items():
    for method, op in item.items():
        if method not in {"get", "post", "put", "delete", "patch"}:
            continue
        assert "responses" in op, (path, method)
        for status, response in op["responses"].items():
            assert "$ref" in response or "description" in response, (path, method, status)
        if path.startswith("/v1/"):
            assert {"401", "403", "500", "503", "422"} <= op["responses"].keys(), (path, method)
        if "requestBody" in op:
            assert "400" in op["responses"], (path, method)
send = spec["components"]["schemas"]["SendRequest"]["properties"]
for field, limit in [("subject", 998), ("from_name", 200)]:
    assert send[field]["x-maxBytes"] == limit
assert send["variables"]["x-maxJSONBytes"] == 262144
events = spec["paths"]["/v1/messages/{id}/events"]["get"]
assert {"cursor", "limit"} <= {p["name"] for p in events["parameters"]}
assert "410" in events["responses"]
assert "sequence" in spec["components"]["schemas"]["Event"]["required"]
print(f"OpenAPI contracts verified: {len(spec['paths'])} paths")
