# Project working preferences

- Do not run Docker commands or Docker-backed checks unless the user explicitly asks. A general request to implement or test changes is not permission to start Docker, manage containers, or run database integration checks against Docker. Prefer local unit tests, vet, and build checks by default.
- Git commits must use the user's configured identity and contain no AI attribution or co-author trailers.
