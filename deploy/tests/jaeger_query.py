import json
from urllib.parse import quote, urlencode


def string_attributes(record):
    return {entry["key"]: entry["value"].get("stringValue") for entry in record.get("attributes", [])}


class JaegerQuery:
    def __init__(self, stack):
        self.stack = stack
        self.port = stack.port("jaeger", 16686)

    def spans(self, path):
        status, _, body = self.stack.http(self.port, "/api/v3/traces" + path)
        if status == 404:
            return []
        if status != 200:
            raise AssertionError(f"Jaeger query returned HTTP {status}")
        payload = json.loads(body)
        if "error" in payload:
            raise AssertionError("Jaeger query reported an error")
        return [
            {**span, "resource": resource["resource"], "scope": scope.get("scope", {})}
            for resource in payload.get("result", {}).get("resourceSpans", [])
            for scope in resource.get("scopeSpans", [])
            for span in scope.get("spans", [])
        ]

    def find(self, service, operation, start, end):
        return self.spans("?" + urlencode({
            "query.serviceName": service,
            "query.operationName": operation,
            "query.startTimeMin": start.isoformat(),
            "query.startTimeMax": end.isoformat(),
        }))

    def get(self, trace_id):
        return self.spans("/" + quote(trace_id, safe=""))


def service_name(span):
    return string_attributes(span["resource"]).get("service.name")
