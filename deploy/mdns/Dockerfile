FROM python:3.12-slim
RUN pip install --no-cache-dir zeroconf==0.136.2
COPY announce.py /app/announce.py
USER nobody
CMD ["python", "/app/announce.py"]
