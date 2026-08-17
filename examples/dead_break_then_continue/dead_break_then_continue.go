package main

func main() {
	go func() {
		for {
			if false {
				break
			}
			continue
		}
	}()
}
