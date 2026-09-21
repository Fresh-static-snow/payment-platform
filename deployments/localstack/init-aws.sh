#!/bin/sh
set -eu

region="${AWS_DEFAULT_REGION:-us-east-1}"
account_id="${AWS_ACCOUNT_ID:-000000000000}"
bucket="${S3_BUCKET:-payment-receipts}"
notification_queue="${SQS_QUEUE_NAME:-payment-notifications}"
dead_letter_queue="${SQS_DLQ_NAME:-payment-dlq}"
topic_name="${SNS_TOPIC_NAME:-payment-notifications}"

if ! awslocal s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
    awslocal s3api create-bucket --bucket "$bucket" >/dev/null
fi

dead_letter_url="$(awslocal sqs create-queue --queue-name "$dead_letter_queue" --query QueueUrl --output text)"
dead_letter_arn="$(awslocal sqs get-queue-attributes --queue-url "$dead_letter_url" --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)"

redrive_attributes="$(DEAD_LETTER_ARN="$dead_letter_arn" python -c 'import json, os; print(json.dumps({"RedrivePolicy": json.dumps({"deadLetterTargetArn": os.environ["DEAD_LETTER_ARN"], "maxReceiveCount": "4"})}))')"
queue_url="$(awslocal sqs create-queue \
    --queue-name "$notification_queue" \
    --attributes "$redrive_attributes" \
    --query QueueUrl --output text)"

topic_arn="$(awslocal sns create-topic --name "$topic_name" --query TopicArn --output text)"
queue_arn="arn:aws:sqs:${region}:${account_id}:${notification_queue}"

policy="{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"sns.amazonaws.com\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${queue_arn}\",\"Condition\":{\"ArnEquals\":{\"aws:SourceArn\":\"${topic_arn}\"}}}]}"
attributes="$(QUEUE_POLICY="$policy" python -c 'import json, os; print(json.dumps({"Policy": os.environ["QUEUE_POLICY"]}))')"
awslocal sqs set-queue-attributes --queue-url "$queue_url" --attributes "$attributes"

subscription="$(awslocal sns list-subscriptions-by-topic --topic-arn "$topic_arn" --query "Subscriptions[?Endpoint=='${queue_arn}'] | [0].SubscriptionArn" --output text)"
if [ -z "$subscription" ] || [ "$subscription" = "None" ]; then
    awslocal sns subscribe \
        --topic-arn "$topic_arn" \
        --protocol sqs \
        --notification-endpoint "$queue_arn" \
        --attributes RawMessageDelivery=true \
        >/dev/null
fi

echo "LocalStack resources ready: s3://${bucket}, ${topic_arn}, ${queue_arn}, ${dead_letter_arn}"
