package router

type Connection interface {
	Request(url string) string
}
