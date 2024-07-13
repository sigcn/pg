package disco

type Controller interface {
	Handle(b []byte)
	Name() string
	Type() uint8
}

type ControllerManager interface {
	Register(Controller)
	Unregister(Controller)
}
