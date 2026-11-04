# python:3.13-slim already ships the openssl binary the backend runs.
FROM python:3.13-slim@sha256:ae14d4ce8771606477a81446e7ef429edd6762305fc50c84507c1041fa8285d1
# Peek mounts a named volume at /data; an owned, empty directory in the image
# makes the volume writable by the non-root user (uid 65532). This backend
# keeps no data, so the line only matters once you add some.
RUN mkdir /data && chown 65532:65532 /data
COPY server.py /app/server.py
# Peek's containers have a read-only root filesystem: no .pyc files, and print
# output goes straight to `peek backend logs`.
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 65532:65532
EXPOSE 8080
CMD ["python", "/app/server.py"]
