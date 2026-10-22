dev:
	air -c .air.toml start

# Build to a temp file then mv so the running binary is never rewritten in place.
build:
	go build -ldflags "-X github.com/fastclaw-ai/weclaw/cmd.Version=$$(cat VERSION)" -o bin/weclaw.new .
	mv -f bin/weclaw.new bin/weclaw

# A rebuilt ad-hoc binary fails macOS launch constraints on its first launchd spawn
# (OS_REASON_CODESIGNING); BTM re-registers it ~10s later, so retry until it runs.
deploy: build
	weclaw restart
	@for i in 1 2 3 4; do \
		sleep 3; \
		if launchctl list | grep -q '^[0-9][0-9]*[[:space:]].*ai\.weclaw\.bridge$$'; then weclaw status; exit 0; fi; \
		[ $$i -eq 4 ] && break; \
		echo "bridge not running (attempt $$i), waiting for launch item re-registration"; \
		sleep 10; weclaw restart; \
	done; \
	echo "bridge failed to start"; exit 1

.PHONY: dev build deploy
