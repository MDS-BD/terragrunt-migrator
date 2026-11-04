# Preserve tabs surrounding all unrelated syntax.
terraform {
	source	=	"git::https://github.com/example-org/terraform-modules.git//ecs-service?ref=ecs-service-5.0.14" # keep tab layout
	other	=	"untouched"
}
