.PHONY: test build smoke up down logs

test:
	./scripts/test.sh

build:
	docker compose build

smoke:
	./tests/container-smoke.sh

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f mihomo
