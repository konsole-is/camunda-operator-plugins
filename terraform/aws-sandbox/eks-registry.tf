# The images of an EKS run: the operator and the aws plugin, built from the
# commit of the run. A run pushes the tag <run-id> and its cleanup deletes it.
# hack/aws-sandbox/eks-sweep.sh and leftovers.sh know these names.
resource "aws_ecr_repository" "eks" {
  for_each = toset(["camunda-operator", "aws"])

  name                 = "cpo-sandbox-eks/${each.key}"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
}

# The last removal of an image when every cleanup of its run failed.
resource "aws_ecr_lifecycle_policy" "eks" {
  for_each = aws_ecr_repository.eks

  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire every image one day after its push."
      selection = {
        tagStatus   = "any"
        countType   = "sinceImagePushed"
        countUnit   = "days"
        countNumber = 1
      }
      action = { type = "expire" }
    }]
  })
}

# The one-time schedules that delete the cluster and the node of a run, also
# when the job of the run is gone.
resource "aws_scheduler_schedule_group" "eks" {
  name = "cpo-sandbox-eks"
}
