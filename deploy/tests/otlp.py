"""Binary OTLP fixture for the mobile trace ingress test."""
from struct import pack
from time import time_ns


def _varint(value):
    result = bytearray()
    while value >= 0x80:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value)
    return bytes(result)


def _bytes_field(number, value):
    return _varint((number << 3) | 2) + _varint(len(value)) + value


def _attribute(key, value):
    any_value = _bytes_field(1, value.encode())
    return _bytes_field(1, key.encode()) + _bytes_field(2, any_value)


def mobile_history_trace(trace_id, root_id, request_id, start_ns, private_canary):
    end_ns = time_ns()

    def span(span_id, name, kind, parent=None):
        data = (
            _bytes_field(1, bytes.fromhex(trace_id))
            + _bytes_field(2, bytes.fromhex(span_id))
            + _bytes_field(5, name.encode())
            + _varint(6 << 3) + _varint(kind)
            + _varint((7 << 3) | 1) + pack("<Q", start_ns)
            + _varint((8 << 3) | 1) + pack("<Q", end_ns)
            + _bytes_field(9, _attribute("url.full", private_canary))
        )
        return data + (_bytes_field(4, bytes.fromhex(parent)) if parent else b"")

    # The ingress must assign the resource identity and strip all unapproved metadata.
    resource = _bytes_field(1, _attribute("service.name", "synthetic-client"))
    root = span(root_id, "machine.history.load", 1)
    request = span(request_id, "GET /v1/machines/{machine_id}/telemetry", 3, root_id)
    scope_spans = _bytes_field(2, root) + _bytes_field(2, request)
    return _bytes_field(1, _bytes_field(1, resource) + _bytes_field(2, scope_spans))
