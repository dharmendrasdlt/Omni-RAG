package main

import (
	"context"
	"log"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	ctx := context.Background()
	log.Println("Starting MongoDB Content Seeder Service...")

	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("MongoDB connection failed: %v", err)
	}
	defer mongoClient.Disconnect(ctx)

	db := mongoClient.Database("content_db")
	mongoColl := db.Collection("articles_v2")

	// Drop previous entries for a clean demonstration run
	mongoColl.Drop(ctx)

	// Fleshed out with explicit semantic terminology to appease the vector geometry math
	rawDocs := []interface{}{
		bson.M{
			"content": "Standard Corporate Working Hours and On-Site Attendance: Regular company operations run from 9 AM to 5 PM, Monday through Friday. All standard full-time employees are expected to be active and reachable during these core business times.",
		},
		bson.M{
			"content": "Flexible Remote Work and Telecommuting Policy: Eligible team members may work remotely or work from home up to 2 business days per calendar week. This arrangement requires direct written manager approval and alignment with team schedules.",
		},
		bson.M{
			"content": "Comprehensive Healthcare and Medical Benefits Package: Group health insurance benefits, including dental and vision insurance coverage, are fully paid and covered for all permanent full-time employees, beginning immediately on their official first day of employment.",
		},
		bson.M{
			"content": "Corporate Expense Reimbursement and Purchase Reconciliation: All business-related expense reimbursement requests and financial receipts must be formally submitted to accounting within 30 days of the initial purchase date. Late submittals may be rejected.",
		},
	}

	_, err = mongoColl.InsertMany(ctx, rawDocs)
	if err != nil {
		log.Fatalf("Failed to seed MongoDB: %v", err)
	}

	log.Println("🎉 Seeding complete! Populated MongoDB content_db.articles_v2 with semantically rich policy blocks.")
}
