FROM python:3.12-slim
WORKDIR /app
COPY sdk/python /sdk/python
RUN pip install --no-cache-dir /sdk/python
COPY examples/06-kubernetes/python /app
ENTRYPOINT ["python", "main.py"]
