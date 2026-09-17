# alg - build both binaries into build/
#
#   make            release build
#   make deb        the installable package: build/alg-control_<version>_amd64.deb
#   make test       unit tests, with the race detector
#   make sim        development build with a simulated EC (see README)

GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all alg gui deb test sim clean

all: alg gui

# The daemon and CLI: pure Go and statically linked, so the process that runs
# as root carries no C libraries and no graphics stack.
alg:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o build/alg ./cmd/alg

# The control panel. Needs a C compiler and the X11/OpenGL headers; the first
# build compiles GLFW and takes a few minutes.
gui:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o build/alg-gui ./cmd/alg-gui

test:
	go vet ./...
	go test -race ./...

sim:
	CGO_ENABLED=0 go build -tags sim -o build/alg-sim ./cmd/alg

# Everything a machine needs, in one file you can double-click. Once you have
# this, the source tree is no longer needed to install or reinstall.
deb: all
	./dist/build-deb.sh

clean:
	rm -rf build
