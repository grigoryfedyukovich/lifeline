package main

func main() {
	go func() {
		for {
			if false {
				return
			}
			continue
		}
	}()
}
