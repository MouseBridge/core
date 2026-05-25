BINARY  := mousebridge
CMD     := ./cmd/mousebridge
INSTALL := /usr/local/bin/$(BINARY)

.PHONY: build test run install clean

build:
	go build -o $(BINARY) $(CMD)

test:
	go test ./...

run: build
	./$(BINARY)

install: build
	cp $(BINARY) $(INSTALL)

clean:
	rm -f $(BINARY)
