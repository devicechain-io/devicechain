# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Sparkplug edge node: Eclipse Tahu's Python library (EPL-2.0) plus edge_node.py.
# Tahu is not vendored in this repository. It is fetched here, at build time, from the
# release tag's commit (v1.0.22), so the image holds exactly that source.
FROM python:3.12-slim@sha256:aa943ce411423c7582288f18707ff66eddcbaeb510502d747a72aeb34f30d159

# Only python/core is used: sparkplug_b.py, sparkplug_b_pb2.py and array_packer.py.
ADD https://github.com/eclipse-tahu/tahu.git#1fc8d9220a6ed7c1f79d8eaae3c460cb681179b0:python/core /tahu/core

COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
# The old generated protobuf code needs the pure-Python implementation on a current Python.
ENV PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python

COPY edge_node.py /app/edge_node.py
USER nobody
CMD ["python", "-u", "/app/edge_node.py"]
