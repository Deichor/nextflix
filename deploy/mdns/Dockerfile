FROM python:3.14.8-slim-trixie
RUN pip install --no-cache-dir zeroconf==0.151.5 ifaddr==0.2.0
COPY announce.py /app/announce.py
USER nobody
CMD ["python", "/app/announce.py"]
