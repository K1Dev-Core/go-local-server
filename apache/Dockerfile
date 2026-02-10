FROM php:8.3-apache

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libzip-dev \
    && docker-php-ext-install mysqli pdo_mysql \
    && a2enmod rewrite \
    && rm -rf /var/lib/apt/lists/*
