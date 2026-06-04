#!/usr/bin/env python3
"""Proxy TCP hôte pour Docker Model Runner.

DMR (Docker Desktop) n'expose son endpoint OpenAI-compatible que sur
127.0.0.1:12434, injoignable depuis les pods k3d. Ce proxy écoute sur
0.0.0.0:12435 et relaie vers 127.0.0.1:12434, de sorte que les pods
l'atteignent via host.k3d.internal:12435 (LLM_URL du déploiement).

Stdlib uniquement, aucune dépendance. Géré par launchd
(deploy/launchd/com.orbinea.dmr-proxy.plist).
"""
import socket
import threading

LISTEN = ("0.0.0.0", 12435)
TARGET = ("127.0.0.1", 12434)


def pipe(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def handle(client):
    try:
        upstream = socket.create_connection(TARGET)
    except OSError:
        client.close()
        return
    threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
    threading.Thread(target=pipe, args=(upstream, client), daemon=True).start()


def main():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(LISTEN)
    srv.listen(128)
    print(f"DMR proxy {LISTEN} -> {TARGET}", flush=True)
    while True:
        client, _ = srv.accept()
        handle(client)


if __name__ == "__main__":
    main()
