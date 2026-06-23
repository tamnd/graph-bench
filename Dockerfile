FROM gcr.io/distroless/static:nonroot
COPY graph-bench /graph-bench
ENTRYPOINT ["/graph-bench"]
