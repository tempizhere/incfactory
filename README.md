# incfactory

A modular Go project for processing, summarizing, and managing card data, with integrations for RabbitMQ, PostgreSQL, and LLM services.

## Project Structure

```
incfactory/
│
├── cmd/                # Main entry points for different services
│   ├── assistant/      # HTTP server for assistant API
│   ├── fetcher/        # Service for fetching data from external sources
│   ├── llm-service/    # Service for LLM (Large Language Model) processing
│   └── processor/      # Service for processing and saving card data
│
├── config/             # Prompt and configuration files (JSON)
│
├── internal/           # Core application logic (internal Go packages)
│   ├── api/            # Kaiten API client and data models
│   ├── db/             # PostgreSQL database connection and operations
│   ├── llm/            # LLM integration, embeddings, and completions
│   ├── queue/          # RabbitMQ queue management
│   └── types/          # Shared data types and structures
│
├── scripts/            # SQL migration scripts
│
├── go.mod              # Go module definition
└── .gitignore          # Git ignore rules
```
