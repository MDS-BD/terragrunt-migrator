# source = "not-lambdas-9.9.9"
module "lambdas" {
   source = "git::https://github.com/example-org/terraform-modules.git//lambdas?ref=lambdas-1.0.25"
   runtime = "nodejs20.x"
}
module "other" {
   source = "git::https://github.com/example-org/terraform-modules.git//lambdas?ref=lambdas-1.0.24"
}
