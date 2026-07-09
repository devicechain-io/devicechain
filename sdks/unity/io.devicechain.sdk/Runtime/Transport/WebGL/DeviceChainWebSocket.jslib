// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Browser-side WebSocket interop for the WebGL build, where System.Net.WebSockets.ClientWebSocket
// does not exist. A poll-based design (C# drains a per-socket queue) is used deliberately — it
// avoids Emscripten dynCall/function-pointer marshalling, which is brittle across Unity/Emscripten
// versions. graphql-transport-ws uses TEXT frames only, so only string messages are queued.
//
// NOTE: authored WITHOUT a WebGL build to test against — verify in a real WebGL player (see the
// package VERIFICATION.md). The paired C# wrapper is WebGlWebSocketConnection.cs.

var DeviceChainWebSocketLib = {
  $dcws: {
    sockets: {},
    nextId: 1,
  },

  // Allocate a socket handle (not yet connected). Returns its id.
  DCWs_Create: function (urlPtr) {
    var url = UTF8ToString(urlPtr);
    var id = dcws.nextId++;
    dcws.sockets[id] = { url: url, ws: null, messages: [], closed: false, closeCode: 0, closeReason: "", errored: false };
    return id;
  },

  // Open the socket, negotiating an optional subprotocol.
  DCWs_Connect: function (id, protoPtr) {
    var s = dcws.sockets[id];
    if (!s) return;
    var proto = protoPtr ? UTF8ToString(protoPtr) : "";
    try {
      s.ws = proto ? new WebSocket(s.url, proto) : new WebSocket(s.url);
      s.ws.onmessage = function (e) {
        if (typeof e.data === "string") { s.messages.push(e.data); }
      };
      s.ws.onclose = function (e) { s.closed = true; s.closeCode = e.code; s.closeReason = e.reason || ""; };
      s.ws.onerror = function () { s.errored = true; };
    } catch (err) {
      s.errored = true;
    }
  },

  // -1 errored, 0 connecting, 1 open, 2 closing, 3 closed (mirrors WebSocket.readyState).
  DCWs_State: function (id) {
    var s = dcws.sockets[id];
    if (!s) return 3;
    if (s.errored) return -1;
    if (!s.ws) return 0;
    return s.ws.readyState;
  },

  DCWs_Send: function (id, msgPtr) {
    var s = dcws.sockets[id];
    if (s && s.ws && s.ws.readyState === 1) { s.ws.send(UTF8ToString(msgPtr)); }
  },

  DCWs_HasMessage: function (id) {
    var s = dcws.sockets[id];
    return (s && s.messages.length > 0) ? 1 : 0;
  },

  // Dequeue the oldest message into a freshly malloc'd UTF-8 buffer; C# reads it then calls DCWs_Free.
  DCWs_TakeMessage: function (id) {
    var s = dcws.sockets[id];
    if (!s || s.messages.length === 0) return 0;
    var msg = s.messages.shift();
    var len = lengthBytesUTF8(msg) + 1;
    var buf = _malloc(len);
    stringToUTF8(msg, buf, len);
    return buf;
  },

  DCWs_Free: function (ptr) {
    if (ptr) { _free(ptr); }
  },

  DCWs_Closed: function (id) {
    var s = dcws.sockets[id];
    return (s && s.closed) ? 1 : 0;
  },

  DCWs_CloseCode: function (id) {
    var s = dcws.sockets[id];
    return s ? s.closeCode : 0;
  },

  // Dequeue the close reason into a freshly malloc'd UTF-8 buffer; C# reads it then calls DCWs_Free.
  DCWs_CloseReason: function (id) {
    var s = dcws.sockets[id];
    var reason = (s && s.closeReason) ? s.closeReason : "";
    var len = lengthBytesUTF8(reason) + 1;
    var buf = _malloc(len);
    stringToUTF8(reason, buf, len);
    return buf;
  },

  DCWs_Destroy: function (id) {
    var s = dcws.sockets[id];
    if (s) {
      try { if (s.ws) { s.ws.close(); } } catch (e) { /* already closing */ }
      delete dcws.sockets[id];
    }
  },
};

autoAddDeps(DeviceChainWebSocketLib, '$dcws');
mergeInto(LibraryManager.library, DeviceChainWebSocketLib);
