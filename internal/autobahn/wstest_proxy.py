# Runs wstest with its WebSocket client sent through a plain HTTP proxy.
#
# Autobahn's fuzzingclient and testeeclient modes have no proxy option. When
# WSTEST_PROXY is host:port, this wrapper opens every client TCP connection to
# that proxy and writes the opening handshake in absolute form
# (GET http://host:port/path HTTP/1.1), which is how Marasi takes plain ws://
# traffic. No CONNECT is sent. Without WSTEST_PROXY it is plain wstest.
import os
import sys

proxy = os.environ.get("WSTEST_PROXY")
if proxy:
    from autobahn.twisted import websocket as twisted_websocket
    from autobahn.websocket import protocol
    from autobahntestsuite import fuzzing, testee

    host, port = proxy.rsplit(":", 1)
    target = {"host": host, "port": int(port)}

    original_connect = twisted_websocket.connectWS

    def connect_ws(factory, *args, **kwargs):
        # connectWS only reads factory.proxy to pick the TCP address. Clearing
        # it again keeps the protocol out of its CONNECT state machine.
        factory.proxy = target
        try:
            return original_connect(factory, *args, **kwargs)
        finally:
            factory.proxy = None

    # The modes look connectWS up in their own module. If a testsuite version
    # stops importing it there, patching would silently do nothing and the
    # run would bypass the proxy.
    for module in (fuzzing, testee):
        assert hasattr(module, "connectWS"), "%s has no connectWS to patch" % module.__name__
        module.connectWS = connect_ws

    original_handshake = protocol.WebSocketClientProtocol.startHandshake

    def start_handshake(self):
        factory = self.factory
        resource = factory.resource
        factory.resource = "http://%s:%d%s" % (factory.host, factory.port, resource)
        try:
            original_handshake(self)
        finally:
            factory.resource = resource

    protocol.WebSocketClientProtocol.startHandshake = start_handshake

from autobahntestsuite.wstest import run

sys.argv[0] = "wstest"
sys.exit(run())
