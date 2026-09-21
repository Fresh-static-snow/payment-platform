FROM migrate/migrate:v4.18.3

COPY --chown=65532:65532 migrations /migrations

USER 65532:65532
ENTRYPOINT ["/migrate"]
