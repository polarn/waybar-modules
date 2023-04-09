.PHONY: build

build:
	go build -o ./build/waybar-gitlab-mr ./cmd/waybar-gitlab-mr

clean:
	rm -rf ./build
