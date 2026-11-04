# source = "fake-component-1.2.3" is only a comment
terraform {
   source   = "git::https://github.com/example-org/terraform-modules.git//ecs-service?ref=ecs-service-5.0.13" # keep spacing
   extra = "untouched"
}
